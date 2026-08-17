# Apollo Audio Controls

Web interface for a Universal Audio Apollo e1x over Dante, without UAD
Console. Gain, 48V, MUTE, polarity, HPF and PAD, plus the XLR input status.

## Why

I think UAD Console is a crappy software, you need several components, etc. With AI and tcpdump I worked out how it talks to the device.

Only tested on an Apollo e1x, and most likely the only thing it works with.

## Running

One Go binary with the interface embedded, no dependencies beyond the standard
library.

```bash
go build -o apollo-audio-controls .
./apollo-audio-controls                 # http://localhost:8090
```

| Flag | |
|---|---|
| `-port` | HTTP port, default 8090 |
| `-listen` | listen address, default all interfaces |
| `-device` | device IP the address field starts out with |
| `-iface` | local interface IP, if the automatic pick is wrong |
| `-lock-48v` | block 48V entirely, including from the interface |

Binaries for macOS (arm64), Linux (amd64, arm64) and Windows are attached to
each release. The container image needs host networking, because control runs
over UDP multicast on the local segment:

```bash
docker run --rm --network host ghcr.io/npetersendk/apollo-audio-controls:latest
```

## Connect and disconnect

The device address is typed into the field in the top bar. Nothing reaches the device until Connect is pressed:

- no session means no sockets, no multicast membership and no packets
- Connect opens the session and sends one status query
- after that, packets are only sent when you press something, and live updates come from the device's own multicasts
- Disconnect, or closing the tab, leaves the multicast group again
- commands without a session are refused with `409`

48V needs a confirmation click, and every write waits for the device to confirm before the interface reports success.

## API

The interface uses nothing the API does not expose.

| | |
|---|---|
| `GET /api/events?device=<ip>` | SSE, the connection that holds the session open |
| `GET /api/state` `GET /api/config` | cached state, no network traffic |
| `POST /api/refresh` | one status query |
| `POST /api/gain` | `{"db":45}` |
| `POST /api/flag` | `{"name":"hpf","on":true}`, 48V requires `"yes":true` |

## Releases

Pushing a `v*.*.*` tag builds and publishes everything: `release.yml` cross compiles the four binaries and publishes them as "Version x.y.z", and `docker-build.yml` builds both architectures, starts the image to check that it serves the interface, and pushes the multi-arch manifest to GHCR. Neither publishes if the tests fail.

## Protocol

[PROTOCOL.md](PROTOCOL.md) documents what the packet captures showed: framing, both vendor blocks, the switch bitfield, how state is read back, and what the device does not expose.

## License

Apache 2.0, see [LICENSE](LICENSE).
