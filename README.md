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

## Step-By-Step Setup

1. Create a shared secret:

```bash
openssl rand -hex 32
```

Use the same value for the broker `--token`, Apps Script `BRIDGE_TOKEN`, and client `--token`.

2. Deploy the broker server.

For Dokploy, create a Docker Compose app from this repository and use the included `docker-compose.yml`. Set these environment variables:

```text
BRIDGE_TOKEN=your-shared-secret
DIAL_NETWORK=tcp4
```

Map your domain, for example `bridge.example.com`, to service `broker` on container port `8080`, then enable HTTPS/Let's Encrypt in Dokploy.

3. Verify the broker health endpoint:

```bash
curl -i https://bridge.example.com/healthz
```

Expected body:

```text
ok
```

4. Configure Apps Script.

Copy [apps-script/Code.gs](/Users/blackestwhite/Desktop/Lab/GAS/apps-script/Code.gs) into a Google Apps Script project and set:

```js
const BROKER_BASE = "https://bridge.example.com";
const BRIDGE_TOKEN = "your-shared-secret";
```

5. Deploy Apps Script as a Web App.

The deployment settings must be:

```text
Execute as: Me
Who has access: Anyone
```

`Anyone` access is required. If it is set to `Only myself`, `Anyone with Google account`, or `User accessing the web app`, the Go client will receive Google login HTML or `401` instead of relay JSON.

6. Verify Apps Script directly or through Google fronting:

```bash
curl --http1.1 \
  "https://script.google.com/macros/s/YOUR_DEPLOYMENT_ID/exec?op=down&bsid=test&ack=0"
```

If direct `script.google.com` is blocked on your network, test fronting:

```bash
curl --http1.1 \
  -H "Host: script.google.com" \
  "https://www.google.com/macros/s/YOUR_DEPLOYMENT_ID/exec?op=down&bsid=test&ack=0"
```

Expected JSON:

```json
{"sid":"test","ack":0,"chunks":[]}
```

7. Run the local SOCKS5 client:

```bash
go run ./cmd/client \
  --listen 127.0.0.1:1080 \
  --relay-url "https://script.google.com/macros/s/YOUR_DEPLOYMENT_ID/exec" \
  --mode socks5 \
  --token "your-shared-secret"
```

If you need Google fronting:

```bash
go run ./cmd/client \
  --listen 127.0.0.1:1080 \
  --relay-url "https://script.google.com/macros/s/YOUR_DEPLOYMENT_ID/exec" \
  --mode socks5 \
  --token "your-shared-secret" \
  --front-dial www.google.com:443 \
  --front-sni www.google.com \
  --front-host script.google.com \
  --poll-timeout 5s
```

8. Test the tunnel:

```bash
curl --socks5-hostname 127.0.0.1:1080 https://api.ipify.org
```

The returned IP should be the broker server's egress IP.

## Deploy Apps Script

1. Open Google Apps Script and create a new project.
2. Replace `Code.gs` with [apps-script/Code.gs](/Users/blackestwhite/Desktop/Lab/GAS/apps-script/Code.gs).
3. Set `BROKER_BASE` to your public broker URL, for example `https://bridge.example.com`.
4. Set `BRIDGE_TOKEN` to the same token used by the Go client and broker.
5. Deploy as a Web App with `Execute as: Me` and `Who has access: Anyone`.
6. Use the Web App `/exec` URL as the client `--relay-url`.

The Go client uses `bsid` as the Apps Script-facing session query parameter by default because Apps Script reserves `sid`. `Code.gs` accepts both `bsid` and legacy `sid`, then forwards `sid` to the broker.

## Run The Broker

Direct mode for SOCKS5 targets:

```bash
go run ./cmd/server --listen :8080 --token change-me --dial-network tcp4
```

Raw mode needs a fixed upstream because raw TCP has no target metadata:

```bash
go run ./cmd/server --listen :8080 --fixed-upstream 127.0.0.1:9000 --token change-me --dial-network tcp4
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
  --front-host script.google.com \
  --poll-timeout 5s
```

This sends TCP/TLS to `www.google.com:443` with SNI `www.google.com`, while the HTTP request still targets `Host: script.google.com` and the Apps Script deployment path. HTTP/1.1 is forced by default when fronting is configured, because it keeps Host-header behavior explicit.

If only `google.com` is reachable on your network, use `--front-dial google.com:443 --front-sni google.com` instead of `www.google.com`.

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

`--poll-timeout` controls the timeout for each downstream poll request. The default is `5s`. Keep this much lower than `--request-timeout` when using Google fronting so one stuck Apps Script/Google edge response does not block downstream data for 20-30 seconds.

`--max-down-batch` controls the broker response size for `/down`. The default is `262144` bytes.

`--session-timeout` controls idle broker cleanup. The default is `60s`.

`--request-timeout` controls client HTTP request timeout. The default is `20s`.

`--dial-network` on the broker controls target dialing: `tcp`, `tcp4`, or `tcp6`. The default is `tcp4` to avoid failed IPv6 target dials on IPv4-only hosts. Use `tcp` only when your broker has working IPv6 routing too.

`--front-dial`, `--front-sni`, and `--front-host` split the outer Google TLS endpoint from the inner Apps Script HTTP host. Use them only when direct `script.google.com` is unavailable but `google.com` or `www.google.com` is reachable.

`--sid-param` controls the Apps Script-facing session query parameter. The default is `bsid`.

`--socks5-reject-ipv6` rejects IPv6 literal SOCKS5 targets locally. The default is `true` because the broker defaults to IPv4-only dialing; set it to `false` only when the broker has working IPv6 routing and runs with `--dial-network tcp`.

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

If logs show `dial tcp4: address 2001:... no suitable address found`, the local application is asking SOCKS5 to connect to an IPv6 literal target while the broker is IPv4-only. Keep `--socks5-reject-ipv6=true` so those impossible requests fail locally and the application can retry an IPv4 target.

If latency is high, reduce `--poll-interval` carefully. This increases request volume and can hit Apps Script quotas faster. If logs show `poll timeout after ...` or `Client.Timeout exceeded while awaiting headers`, reduce `--poll-timeout` to fail that stuck Google poll faster and retry a new one.

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
