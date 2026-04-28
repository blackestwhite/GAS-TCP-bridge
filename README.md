# gas-tcp-bridge

`gas-tcp-bridge` is a TCP-over-HTTP relay built around Google Apps Script Web App limitations. Apps Script is treated as a dumb HTTPS relay only; the Go client and Go broker implement sessions, chunking, ACKs, retries, polling, and TCP bridging.

Traffic flow:

```text
local app/browser
  -> local Go TCP forwarder
  -> HTTPS requests to Google Apps Script Web App
  -> Apps Script UrlFetchApp relay
  -> Go broker server
  -> upstream TCP target / SOCKS5 target / HTTP CONNECT / Xray inbound
```

## Why Apps Script Is Only A Relay

Google Apps Script Web Apps cannot provide raw TCP, WebSocket tunneling, HTTP CONNECT, or true streaming responses. This project does not try to force it into those roles. Apps Script receives normal HTTP requests and forwards them with `UrlFetchApp.fetch`; all connection semantics live in the Go client and broker.

## Deploy Apps Script

1. Open Google Apps Script and create a new project.
2. Replace `Code.gs` with [apps-script/Code.gs](/Users/blackestwhite/Desktop/Lab/GAS/apps-script/Code.gs).
3. Set `BROKER_BASE` to your public broker URL, for example `https://bridge.example.com`.
4. Set `BRIDGE_TOKEN` to the same token used by the Go client and broker.
5. Deploy as a Web App with access set according to your needs.
6. Use the Web App `/exec` URL as the client `--relay-url`.

The Go client uses `bsid` as the Apps Script-facing session query parameter by default because Apps Script reserves `sid`. `Code.gs` accepts both `bsid` and legacy `sid`, then forwards `sid` to the broker.

## Run The Broker

Direct mode for SOCKS5 targets:

```bash
go run ./cmd/server --listen :8080 --token change-me
```

Raw mode needs a fixed upstream because raw TCP has no target metadata:

```bash
go run ./cmd/server --listen :8080 --fixed-upstream 127.0.0.1:9000 --token change-me
```

With Docker Compose:

```bash
BRIDGE_TOKEN=change-me docker compose up --build
```

## Run The Local Client

SOCKS5 mode:

```bash
go run ./cmd/client \
  --listen 127.0.0.1:1080 \
  --relay-url "https://script.google.com/macros/s/XXX/exec" \
  --mode socks5 \
  --token change-me
```

Raw TCP mode:

```bash
go run ./cmd/client \
  --listen 127.0.0.1:18080 \
  --relay-url "https://script.google.com/macros/s/XXX/exec" \
  --mode raw \
  --token change-me
```

## Fronted Google Transport

If direct `script.google.com` is reset but `google.com` or `www.google.com` is reachable, keep the logical relay URL as the Apps Script URL and override only the TCP/TLS front:

```bash
go run ./cmd/client \
  --listen 127.0.0.1:1080 \
  --relay-url "https://script.google.com/macros/s/XXX/exec" \
  --mode socks5 \
  --token change-me \
  --front-dial www.google.com:443 \
  --front-sni www.google.com \
  --front-host script.google.com
```

This sends TCP/TLS to `www.google.com:443` with SNI `www.google.com`, while the HTTP request still targets `Host: script.google.com` and the Apps Script deployment path. HTTP/1.1 is forced by default when fronting is configured, because it keeps Host-header behavior explicit.

## Example Raw TCP Usage

If the broker is started with `--fixed-upstream 127.0.0.1:9000`, any bytes sent to the local listener are relayed to that upstream:

```bash
printf "hello" | nc 127.0.0.1 18080
```

## Example SOCKS5 Usage

Point a SOCKS5-capable client at the local listener:

```bash
curl --socks5-hostname 127.0.0.1:1080 https://example.com/
```

The client performs a minimal SOCKS5 no-auth CONNECT handshake, sends an `open` message with the requested host and port, then relays TCP chunks.

## Tuning Options

`--chunk-size` controls how TCP data is split before JSON/base64 encoding. The default is `16384`.

`--poll-interval` controls client downstream polling. The default is `100ms`; lower values reduce latency but increase Apps Script and broker request volume.

`--max-down-batch` controls the broker response size for `/down`. The default is `262144` bytes.

`--session-timeout` controls idle broker cleanup. The default is `60s`.

`--request-timeout` controls client HTTP request timeout. The default is `20s`.

`--front-dial`, `--front-sni`, and `--front-host` split the outer Google TLS endpoint from the inner Apps Script HTTP host. Use them only when direct `script.google.com` is unavailable but `google.com` or `www.google.com` is reachable.

`--sid-param` controls the Apps Script-facing session query parameter. The default is `bsid`.

## Known Limitations

- This is not true streaming; data moves in HTTP request/response chunks.
- Latency is bounded by polling interval, Apps Script execution time, and network RTT.
- Google Apps Script quotas and execution limits make this unsuitable for high bandwidth.
- UDP is not supported.
- WebSocket is not supported.
- HTTP CONNECT is not implemented in Apps Script; CONNECT-capable tools should use the local SOCKS5 listener or a fixed upstream such as Xray.
- Payload bytes are base64 encoded inside JSON, which adds overhead.
- Fronted Google transport depends on current Google edge routing behavior and is not a stable API contract.

## Troubleshooting

If the broker returns `unauthorized`, verify `--token` on client and broker and `BRIDGE_TOKEN` in Apps Script.

If raw mode fails with `target host and port are required`, start the broker with `--fixed-upstream host:port`.

If SOCKS5 connects but traffic stalls, check that the broker can reach the requested target host and port directly.

If latency is high, reduce `--poll-interval` carefully. This increases request volume and can hit Apps Script quotas faster.

If sessions linger, reduce `--session-timeout` on the broker.

If direct Apps Script fails with `connection reset by peer`, test fronting manually:

```bash
curl --http1.1 -H 'Host: script.google.com' \
  'https://www.google.com/macros/s/INVALID_DEPLOYMENT_ID/exec?op=down&bsid=test&ack=0'
```

If that returns an Apps Script/Drive-style `404`, fronting is likely viable on your network.

## Development

```bash
make build
make test
make run-server
RELAY_URL="https://script.google.com/macros/s/XXX/exec" make run-client
```
