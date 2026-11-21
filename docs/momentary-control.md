# Set an output using a momentary FBD signal

This guide applies to the observed SR3B261FU + SR3XT141FU in FBD RUN mode
through SR2USB01. Reading a front-panel state and initializing Monitoring may
work differently on other relays, firmware and programs.

A momentary signal injects one event into an existing FBD link. Choose a
link that already toggles the required output through the same logic as the
wall switch. A reset link that always clears an output cannot implement
both ON and OFF. A maintained force may override wall switches and can
survive a lost connection. Use the momentary path for an automation whose
local controls should continue to operate normally.

## Verify the mapping

1. Save a copy of the application and keep the PLC in RUN. Connect Zelio
   Soft 2 and start recording the SR2USB01 controller with USBPcap in
   Wireshark before entering Monitoring. Use the application that matches
   the installed relay and leave the program in the module intact.
2. Click the command input link for a single intended light or output
   group. Compare the outputs in RUN I/O and check the actual load.
   Repeat to restore the starting state. For a group, confirm that one
   click changes every member and does not change unrelated outputs.
3. Find the corresponding write request to region `0x78`.
   [The offline decoder](../scripts/usbpcap_z2_frames.py) shows the
   outgoing frames and their timestamps:

   ```sh
   python3 scripts/usbpcap_z2_frames.py capture.pcapng --writes-only --region 0x78
   ```

   The payload is `E0 <signal ID> 00 01`. The signal ID refers to a
   link in this compiled application. Reverify it after a new program,
   relay or firmware change. An ACK alone cannot establish the mapping.
4. Reconnect the cable to the bridge host. Stop Zelio Soft and other
   clients before opening that cable. Use `zelio monitor-io --serial SERIAL`
   and `zelio monitor-status --serial SERIAL` to make read-only checks
   before starting the bridge.

## Configure the bridge

Start from [the sample configuration](../examples/momentary.config.json).
Its signal IDs and I/O labels are placeholders. Set the actual cable
serial and broker details. Replace each sample ID with the verified ID
expressed as a decimal number. Use `label` for one output or `labels`
for a group that always changes together. Use optional `block_when` for
physical input labels that prevent a remote command, such as a master input
in the program you are integrating.

```sh
bin/zelio validate --config /path/to/config.json
bin/zelio serve --config /path/to/config.json
```

The bridge advertises controlled channels as MQTT switches. It sends at
most one pulse for a desired state, waits for RUN I/O readback, and
publishes the observed state. An incomplete or disagreeing group is
unavailable. Up to two blank LCD redraw frames are ignored at polling
intervals of two seconds or less. Other missing I/O pages and lost
transport are reported immediately. A lost USB or MQTT session
disarms live commands for that bridge process. Verify the connection and
restart the service before issuing new commands.

For Home Assistant scripts, skip switches already in the desired state.
Send the remaining commands **in order**, wait for readback and leave
time for the bridge to finish publishing before sending the next. Busy
commands can still be dropped, so stop on a readback timeout and inspect
the relay before retrying. An
[example script](../examples/home-assistant-scripts.yaml) shows the
sequence. To match an arrival scene that runs only when all lights are
off, add a condition that checks all involved switches are explicitly
`off`; an unavailable state must fail the condition.

A physical master input may reset many Q outputs with a single command.
Verify that separately from any toggle group. The MQTT switch accepts a
desired state for a toggle group; a reset-only command belongs in a separate
explicit action that checks which outputs it affects and confirms OFF.

## Connection after power loss

In the observed installation the relay came back in RUN after a power
interruption and continued handling wall switches. Its Monitoring status
read `81 00`. Command frames could still receive ACKs while leaving their
outputs unchanged. A Zelio Soft capture of entering Monitoring showed an
initialization write, then a status of `83 00` and successful readback
after a pulse.

The Go client uses only the captured initialization write. It checks for
the observed inactive status and RUN, waits for the active status and
compares logical outputs before and after initialization. Unknown statuses
and unconfirmed writes fail the operation. The automatic path was checked
with simulated wire replies. The init sequence and successful pulse were
captured on hardware in Zelio Soft. A complete cold start using only the Go
client still needs a controlled hardware test.

After a service outage, correlate host reboot and Wi-Fi logs with MQTT state
and a PLC read. Use a persistent system journal on Linux so the preceding
boot survives a restart. A Linux host can be reachable while PLC pulse
writes still require Monitoring initialization.

## Program-owned SL IN/OUT as an alternative

A documented alternative for an application you can edit is to add its own
SL IN command path. One tested design arms a command, consumes it once and
provides a separate SL OUT acknowledgment. Use a rising-edge trigger so a
stale value cannot keep commanding the output. Keep the physical button in
the normal FBD logic and verify both initialized restart and interrupted
commands on your hardware. The sample bridge uses momentary links. SL IN
word allocation and handshake values depend on your application.
