# tiny-apps

Small, single-purpose apps I actually run. One directory per app under `apps/`,
each self-contained: its own toolchain, its own `Makefile`, its own deploy
manifests. Nothing is shared between them on purpose — a tiny app that needs a
shared library is not tiny any more.

Everything here is personal infrastructure. Single user, single replica,
deployed to a home k3s cluster or run on a laptop. None of it is built to
survive load, and none of it is built for anyone else.

## Apps

| App | What it does | Runtime | Status |
| --- | --- | --- | --- |
| [pombo](apps/pombo) | WhatsApp bot for the family groups. Files photos and PDFs sent to the documents group in paperless, grouped into documents by a vision LLM. | Go + SQLite | running |

## Conventions

Each app follows the same shape so switching between them costs nothing:

```
apps/<name>/
  README.md          what it is, how to run it, how to deploy it
  Makefile           build / test / run / image — same targets everywhere
  deploy/            k8s manifests, or a compose file, or nothing
  *.example.*        config templates; the real config is gitignored
```

Shared rules:

- **No secrets, no personal data in git.** Config that contains either ships as
  `something.example.yaml` and the real file is gitignored. If an app needs real
  data to be useful, it reads it from a path outside the repo or from a k8s
  Secret/ConfigMap.
- **Config in files, not in a database.** Config changes are a git commit and a
  restart. That is the whole change-management story.
- **One process, one datastore, no queue.** SQLite on a volume until proven
  otherwise.
- **Downtime is fine.** Single replica, `Recreate` strategy, no HA.

## Root targets

```
make test    # run every app's tests
make build   # build every app
make lint    # format check + vet every app
make fmt     # format every app
make list    # which apps exist
```

Each target loops over `apps/*/Makefile` and delegates. An app without a
`Makefile` is skipped, and CI runs the same targets.
