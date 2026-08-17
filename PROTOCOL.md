# The Apollo e1x control protocol

Reverse engineered by capturing UAD Console's network traffic against an
Apollo e1x on a live Dante network. Everything here was verified against real
hardware: the packets this tool sends are byte identical to Console's, and the
device confirms every change.

## Transport

Control is UDP only. The device has no open TCP ports at all (a full connect
scan of 1-65535 returned nothing).

| Direction | Endpoint | Purpose |
|---|---|---|
| Host to device | `<device>:8700` | commands (Dante CMC) |
| Device to multicast | `224.0.0.231:8702` | state changes |
| Device to multicast | `224.0.0.233:8708` | Dante status, periodic |

Console sends from source port 8700, but the device does not validate the
source port, so any ephemeral port works. That matters in practice, because
`UA Mixer Engine` holds 8700 for as long as the UA software is running.

There is no authentication. Anyone on the Dante network can control the
device.

## Frame format

Every vendor message shares one frame:

```
offset  size  field
  0      2    ff ff                  magic
  2      2    length (big endian)    whole message
  4      2    sequence number        counts up per sender
  6      2    00 00
  8      8    sender ID              MAC + 00 00, devices use EUI-64
 16      8    vendor ID              "UnvAudio" or "Audinate" (ASCII)
 24      -    vendor body
```

The parameters are split across two vendor blocks, which is the least obvious
part of the protocol and cost a wasted round of measurements to discover:

- gain sits in UA's own `UnvAudio` block
- the switches sit in the `Audinate` block

## Gain: `UnvAudio`

SET body, sent unicast to `:8700`:

```
01 02 | 00 06 | 00 04 | 43 28 40 VV
 cmd    len=6   len=4   parameter blob
```

Nested TLV: `00 06` is the length of the rest, `00 04` of the blob itself.

```
dB = VV + 9          VV in 0x01..0x38   ->   10..65 dB
```

Linear, 1 dB per step. Both endpoints were verified against Console's own
display. The device multicasts the raw blob `43 28 40 VV` (without the
`01 02` header) to `224.0.0.231:8702` roughly 2 ms later.

## Switches: `Audinate`

A bitfield written as a mask/value pair:

```
07 39 01 40 | 00 0f 42 40 | 00 01 00 01 | 00 08 00 10 | 0000 00MM | 0000 00VV
  opcode      1000000 us    ^^ 01=write                   mask       value
```

| Bit | Mask | Function |
|---|---|---|
| 0 | `0x01` | 48V phantom power |
| 1 | `0x02` | MUTE |
| 2 | `0x04` | polarity invert |
| 3 | `0x08` | HPF / low cut |
| 4 | `0x10` | PAD |

All five were verified by correlating bit changes with actual button presses,
and then by setting them and getting the device's confirmation back.

## Reading state

The same message with the write flag set to `00` is a plain query. The device
usually answers within 2 ms:

```
07 39 01 41 ... | 00 00 00 DD | 00 00 00 3f | 00 00 00 FF
                     detect        cap          flags
```

| Field | |
|---|---|
| `detect` | `0x80` means something is plugged into the XLR input, `0x00` empty |
| `cap` | constant `0x3f`, probably which functions the device supports |
| `flags` | the current bitmap from the table above |

MIC/LINE is not a control. The device detects what is plugged in and reports
it in `detect`; Console only displays it. That is why it never appeared as a
command on the wire. Verified by pulling the XLR connector four times and
watching `detect` follow along every time.

## The sequence number matters

The device rejects a message if it has just seen the same sequence number.
Two processes that both start their counter from something like
`int(time.time())` will have the second command silently dropped, with no
error. Start the counter at random.

This cost a debugging round to find, because the symptom looks like rate
limiting: the first command works, the next one does not.

## Auto-mute

Console sets the MUTE bit itself around certain changes and clears it again
afterwards:

- on a 48V change: muted for about 3.5 seconds
- on an HPF change: muted for about 1 second
- on polarity and PAD: no mute

It is pop protection. Worth knowing if you poll the bitmap, because a MUTE you
did not set can be the device protecting itself.

## Limitations

**Gain cannot be read back.** The query returns the switches and the input
status, but not the gain value. Gain is only sent on changes, so a reader has
to wait for someone to touch the control. It is also why UAD Console can show
a different gain than the device is actually set to: Console shows its own
last known value and never reads back.

**There is no metering.** The periodic broadcasts on `224.0.0.233:8708` are
`Audinate` vendor messages, and the bytes that vary behave like counters and
timestamps, not audio levels. Console does not show metering for the device
either, and Console is the reference implementation, so the device does not
expose it. Levels have to be measured on the Dante audio flow itself, not
through the control protocol.

**Channel 1 only.** The gain blob `43 28 40` appears to contain a channel
selector in `43 28`, but that has not been tested against more channels.

**The `cap` field** is always `0x3f`. The assumption that it is a capability
mask is unconfirmed, since it has never been seen to change.

## Verification

The Go tests in this repository compare the packets the tool builds against
bodies lifted straight out of the original captures, so a change that breaks
the encoding fails the build. The captures and the Python tool used for the
mapping are kept locally in `python/` and are not part of the repository.
