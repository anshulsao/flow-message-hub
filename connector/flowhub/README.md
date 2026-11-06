# flowhub — flow human connector

Makes the hub a first-class [flow human connector](https://github.com/Facets-cloud/flow) (contract v1).

```
agent: flow message user "q" --options "A|B"
  └► flow dispatch ─► connector/flowhub/run send ─► curl POST 127.0.0.1:${FLOWHUB_PORT:-8787}/api/connector/ask
                                                       (X-Flow-Connector: 1, body = ask JSON, no token)
hub Inbox: shows the ask + option buttons, browser notification if the tab is open
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

## Endpoint guard

`POST /api/connector/ask` refuses any request carrying an `Origin` header,
lacking `X-Flow-Connector: 1`, or from a non-loopback peer. It only records
the ask id for the UI's notification; it never writes to flow.
