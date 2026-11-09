# flowhub — flow human connector

Makes the hub a first-class [flow human connector](https://github.com/Facets-cloud/flow) (contract v1).

```
agent: flow message user "q" --options "A|B"
  └► flow dispatch ─► connector/flowhub/run send ─► curl POST 127.0.0.1:${FLOWHUB_PORT:-8787}/api/connector/ask
                                                       (X-Flow-Connector: 1, body = ask JSON, no token)
hub Inbox: shows the ask + option buttons, browser notification if the tab is open
  no hub tab polled /api/inbox in the last 10s ─► 503 {"ok":false,"reason":"no open hub tab"}
                                                 ─► run exits 1 ─► flow falls back to the next connector
  click/reply ─► FLOW_CONNECTOR_TOKEN=<secret> flow human reply <ask-id> "<text>"   (answered, reply_to=<id>)
  open thread ─► FLOW_CONNECTOR_TOKEN=<secret> flow human seen <id>
```

## Install

Start the hub first (`add` runs `run test`, which must reach it), then:

```
flow connector add /path/to/flow-hub-live/connector/flowhub
```

The hub finds the secret flow generated the same way flow does: keychain
item service `flow-connector-flowhub`, account `flow-<12 hex sha256(FLOW_ROOT)>`,
else `$FLOW_ROOT/secrets/flowhub`. `FLOW_ROOT` is the directory of the
`flow.db` the hub reads. Nothing restarts — the secret is read per write.

Remove with `flow connector remove flowhub`. Without the connector the hub
still works: replies go out as plain `flow message user/<task>` and opening
a thread can't mark mail seen (the UI says so).

## Only claims an ask when a tab is open

`run send` exits 0 only when a human can actually see the ask. The hub
remembers the last time a tab polled `/api/inbox` (the Graph page's Inbox
polls every 2s); an ask arriving more than 10s after that (tune with
`-live-window`) gets 503, isn't recorded, and `run` exits 1 — so flow's
connector fallback moves on to the next connector (e.g. the macOS dialog).
The Channels view doesn't poll the Inbox and can't show asks, so it doesn't
count. `run test` (kind `test`) still answers 200 whenever the hub is up, so
`flow connector add` works without a tab open.

## Endpoint guard

`POST /api/connector/ask` refuses any request carrying an `Origin` header,
lacking `X-Flow-Connector: 1`, or from a non-loopback peer. It only records
the ask id for the UI's notification; it never writes to flow.
