# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

**Read [`STATUS.md`](STATUS.md) first.** It holds the current state, the next steps and the conventions of this fork (branches, versions, changelog, pull requests). Update it after every step, so the next session can continue without the chat history.

The add-on lives in `max2mqtt/` (Go). Build and test there:

```bash
cd max2mqtt
go vet ./... && go test ./... && go build ./...
```
