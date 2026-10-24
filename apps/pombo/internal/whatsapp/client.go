// Package whatsapp connects to WhatsApp as a linked device (via whatsmeow)
// and turns group messages into router.Messages.
package whatsapp

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/store/sqlstore"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	waLog "go.mau.fi/whatsmeow/util/log"
	"google.golang.org/protobuf/proto"

	"github.com/andreroggeri/pombo/internal/router"

	_ "modernc.org/sqlite"
)

// Status is what the dashboard shows about the WhatsApp connection.
type Status struct {
	LoggedIn  bool
	Connected bool
	Phone     string // the bot's own number, once paired
	QRCode    string // raw QR payload while waiting to be paired
}

type Group struct {
	JID  string
	Name string
}

type Client struct {
	cli       *whatsmeow.Client
	onMessage func(context.Context, router.Message)
	log       *slog.Logger

	mu sync.Mutex
	qr string

	// LoggedOut is closed when the device is unlinked from the phone. The
	// session can't be reused after that; the process should exit and pair again.
	LoggedOut  chan struct{}
	logoutOnce sync.Once
}

// New opens (or creates) the session database at dbPath.
func New(ctx context.Context, dbPath string, onMessage func(context.Context, router.Message), log *slog.Logger) (*Client, error) {
	waLogger := waLog.Stdout("whatsmeow", "WARN", false)
	container, err := sqlstore.New(ctx, "sqlite",
		"file:"+dbPath+"?_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)", waLogger)
	if err != nil {
		return nil, fmt.Errorf("open session store: %w", err)
	}
	device, err := container.GetFirstDevice(ctx)
	if err != nil {
		return nil, fmt.Errorf("load device: %w", err)
	}
	c := &Client{
		cli:       whatsmeow.NewClient(device, waLogger),
		onMessage: onMessage,
		log:       log,
		LoggedOut: make(chan struct{}),
	}
	c.cli.AddEventHandler(c.onEvent)
	return c, nil
}

// Start connects. If the device isn't paired yet, it keeps a fresh QR code
// available for the dashboard until someone scans it.
func (c *Client) Start(ctx context.Context) error {
	if c.cli.Store.ID != nil {
		return c.cli.Connect()
	}
	go c.pairLoop(ctx)
	return nil
}

func (c *Client) Stop() { c.cli.Disconnect() }

func (c *Client) pairLoop(ctx context.Context) {
	for ctx.Err() == nil && c.cli.Store.ID == nil {
		qrChan, err := c.cli.GetQRChannel(ctx)
		if err != nil {
			c.log.Error("get QR channel", "err", err)
			return
		}
		if err := c.cli.Connect(); err != nil {
			c.log.Error("connect for pairing", "err", err)
			time.Sleep(10 * time.Second)
			continue
		}
		for item := range qrChan {
			switch item.Event {
			case whatsmeow.QRChannelEventCode:
				c.setQR(item.Code)
			case whatsmeow.QRChannelSuccess.Event:
				c.setQR("")
				c.log.Info("paired with phone")
				return
			default:
				c.setQR("")
				c.log.Warn("pairing attempt ended", "event", item.Event, "err", item.Error)
			}
		}
		// The channel closes when its codes run out; start over with new ones.
		c.cli.Disconnect()
	}
}

func (c *Client) setQR(code string) {
	c.mu.Lock()
	c.qr = code
	c.mu.Unlock()
}

func (c *Client) Status() Status {
	c.mu.Lock()
	defer c.mu.Unlock()
	s := Status{LoggedIn: c.cli.Store.ID != nil, Connected: c.cli.IsConnected(), QRCode: c.qr}
	if s.LoggedIn {
		s.Phone = c.cli.Store.ID.User
	}
	return s
}

// Groups lists the groups the bot's number is a member of.
func (c *Client) Groups(ctx context.Context) ([]Group, error) {
	if !c.cli.IsLoggedIn() {
		return nil, nil
	}
	infos, err := c.cli.GetJoinedGroups(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]Group, 0, len(infos))
	for _, g := range infos {
		out = append(out, Group{JID: g.JID.String(), Name: g.Name})
	}
	return out, nil
}

func (c *Client) onEvent(evt any) {
	switch e := evt.(type) {
	case *events.Message:
		// Handlers can take minutes (paperless + LLM); don't block whatsmeow.
		go c.handleMessage(e)
	case *events.Connected:
		c.log.Info("connected to WhatsApp")
	case *events.Disconnected:
		c.log.Warn("disconnected from WhatsApp")
	case *events.LoggedOut:
		c.log.Error("device was unlinked from the phone", "reason", e.Reason)
		c.logoutOnce.Do(func() { close(c.LoggedOut) })
	}
}

func (c *Client) handleMessage(e *events.Message) {
	if !e.Info.IsGroup || e.Info.IsFromMe {
		return
	}
	ctx := context.Background()
	m := router.Message{
		ChatJID:     e.Info.Chat.String(),
		SenderPhone: c.senderPhone(ctx, e.Info),
		Text:        messageText(e.Message),
		Media:       c.media(e.Message),
		ReplyFunc: func(ctx context.Context, text string) error {
			_, err := c.cli.SendMessage(ctx, e.Info.Chat, replyTo(e, text))
			return err
		},
		ReactFunc: func(ctx context.Context, emoji string) error {
			_, err := c.cli.SendMessage(ctx, e.Info.Chat, c.cli.BuildReaction(e.Info.Chat, e.Info.Sender, e.Info.ID, emoji))
			return err
		},
	}
	c.onMessage(ctx, m)
}

// senderPhone returns the sender's phone number. In many groups WhatsApp
// identifies senders by an anonymous LID instead; map it back to a number.
func (c *Client) senderPhone(ctx context.Context, info types.MessageInfo) string {
	if info.Sender.Server != types.HiddenUserServer {
		return info.Sender.User
	}
	if info.SenderAlt.Server == types.DefaultUserServer {
		return info.SenderAlt.User
	}
	pn, err := c.cli.Store.LIDs.GetPNForLID(ctx, info.Sender)
	if err != nil || pn.IsEmpty() {
		c.log.Warn("could not resolve sender phone from LID", "lid", info.Sender, "err", err)
		return ""
	}
	return pn.User
}

func messageText(m *waE2E.Message) string {
	switch {
	case m.GetConversation() != "":
		return m.GetConversation()
	case m.GetExtendedTextMessage() != nil:
		return m.GetExtendedTextMessage().GetText()
	case m.GetImageMessage() != nil:
		return m.GetImageMessage().GetCaption()
	case m.GetDocumentMessage() != nil:
		return m.GetDocumentMessage().GetCaption()
	}
	return ""
}

func (c *Client) media(m *waE2E.Message) *router.Media {
	if img := m.GetImageMessage(); img != nil {
		return &router.Media{
			MimeType: img.GetMimetype(),
			Download: func(ctx context.Context) ([]byte, error) { return c.cli.Download(ctx, img) },
		}
	}
	if doc := m.GetDocumentMessage(); doc != nil {
		return &router.Media{
			FileName: doc.GetFileName(),
			MimeType: doc.GetMimetype(),
			Download: func(ctx context.Context) ([]byte, error) { return c.cli.Download(ctx, doc) },
		}
	}
	return nil
}

// replyTo builds a text message quoting the original.
func replyTo(e *events.Message, text string) *waE2E.Message {
	return &waE2E.Message{
		ExtendedTextMessage: &waE2E.ExtendedTextMessage{
			Text: proto.String(text),
			ContextInfo: &waE2E.ContextInfo{
				StanzaID:      proto.String(e.Info.ID),
				Participant:   proto.String(e.Info.Sender.ToNonAD().String()),
				QuotedMessage: e.Message,
			},
		},
	}
}
