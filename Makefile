APPS := $(patsubst apps/%/Makefile,%,$(wildcard apps/*/Makefile))

.PHONY: test build fmt lint list $(APPS)

test build fmt lint:
	@for app in $(APPS); do \
		echo "==> $$app: $@"; \
		$(MAKE) -C apps/$$app $@ || exit 1; \
	done

list:
	@echo $(APPS)
