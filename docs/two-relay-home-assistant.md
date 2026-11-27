# Two Zelio relays in Home Assistant and Alice

[Читать по-русски](two-relay-home-assistant.ru.md)

Give each Zelio relay its own SR2USB01 USB cable, MQTT device ID and bridge
process. Both processes can use the broker configured in Home Assistant.
Build the CLI and check USB access on the host with the cables.
[Discover a relay over USB](discover.md) · [Schneider Electric SR2USB01](https://www.se.com/uk/en/product/SR2USB01/usb-pc-connecting-cable-for-smart-relay-zelio-logic-3-m/)

## Discover and run the second relay

1. Leave the first relay's existing MQTT device ID in place. The examples
   use `zelio_a` for that device and a new ID, `zelio_b`, for the second.
2. Connect the second cable, set the second relay to its RUN I/O page and
   run `bin/zelio list`. Match the serial to the second cable. Each cable
   needs exclusive USB access. Stop any process using the second cable
   before running discovery on it.
3. Replace `SECOND_CABLE_SERIAL` and `BROKER_URL` with your values and
   generate a read-only configuration. Keep the report locally.

```sh
bin/zelio discover --serial SECOND_CABLE_SERIAL --device-id zelio_b \
  --mqtt-url BROKER_URL --config-out relay-b-readonly.json > relay-b.report.json
bin/zelio validate --config relay-b-readonly.json
bin/zelio monitor-io --serial SECOND_CABLE_SERIAL
```

Check the second relay's logical outputs, then test each candidate for your
lights using the single-route pilot in the discovery guide. A verified
route that toggles a track group produces one MQTT switch with all of its
`Q` labels. Use that switch as one channel. The example below selects one
tested route. Set `PROGRAM_SHA256` and `TRACKS_SIGNAL` from the second relay's
report. Repeat `--verified-signal` for additional tested routes with
disjoint output sets. Include only reviewed lighting routes for this setup.
[Verify one route](discover.md#test-one-toggle-before-deployment)

```sh
bin/zelio discover --serial SECOND_CABLE_SERIAL --device-id zelio_b \
  --mqtt-url BROKER_URL --expect-program PROGRAM_SHA256 \
  --verified-signal TRACKS_SIGNAL --config-out relay-b.json
bin/zelio validate --config relay-b.json
```

Keep the first config and install the second as a separate service. With
the systemd template already installed, this example uses
`zelio@relay_a` for the first instance and `zelio@relay_b` for the second.
Their files are `/etc/zelio/relay_a.json` and
`/etc/zelio/relay_b.json`. If your broker uses credentials, set the
environment variables for the second service and grant its MQTT topic
access. On macOS, run each config in its own `zelio serve` process.
[Service setup](install.md#linux-systemd) · [MQTT access](install.md#mqtt-setup)

```sh
sudo install -m 0640 -o root -g zelio relay-b.json /etc/zelio/relay_b.json
sudo systemctl enable --now zelio@relay_b
sudo journalctl -u zelio@relay_b -n 30 --no-pager
```

In Home Assistant, open **Settings → Devices & services → Entities**.
Find the verified switches for both relay devices and use their actual
entity IDs in the examples below. Test each switch in Home Assistant
before exposing it to Alice. Keep fans and other non-light loads out of
the lighting list.

## Show a switch as a light

You can expose a reviewed `switch.*` directly, or wrap it as a
`light.*` when you want it categorized as lighting in Alice. Add this
entry to the existing `template:` section of `configuration.yaml`, and
replace the source switch ID. The light follows the source switch's
availability and reported state.
[Home Assistant template lights](https://www.home-assistant.io/integrations/template/#light)

```yaml
template:
  - light:
      - name: Track lights
        unique_id: zelio_b_track_lights
        default_entity_id: light.zelio_b_tracks
        availability: "{{ has_value('switch.zelio_b_tracks') }}"
        state: "{{ is_state('switch.zelio_b_tracks', 'on') }}"
        optimistic: false
        turn_on:
          - action: switch.turn_on
            target:
              entity_id: switch.zelio_b_tracks
        turn_off:
          - action: switch.turn_off
            target:
              entity_id: switch.zelio_b_tracks
```

For the track group, create one light from its one discovered switch.
If you use the light wrapper, select it as the Alice entity for that
load. Keep the source switch available to Home Assistant scripts.

## Turn off lights on both relays

Add both scripts to `scripts.yaml` if it is included under `script:`,
or under the existing `script:` key in `configuration.yaml`. Replace
the two lists with your reviewed switch IDs. The script issues OFF only
to channels currently ON. Each branch waits for logical OFF and pauses
briefly before moving to the next channel on that relay. The two relay
branches can run in parallel because they use separate bridge processes.

A timeout lets each branch continue with its remaining lights. The final
check marks the script failed if any listed switch still lacks a confirmed
OFF state. An unavailable or mixed track group therefore needs investigation
before you try it again. Check the script trace and the relay's I/O.
[Home Assistant scripts](https://www.home-assistant.io/docs/script/) · [Wait for readback](https://www.home-assistant.io/building-blocks/wait_template/)

```yaml
zelio_off_in_order:
  alias: Set one Zelio relay's lights to OFF
  mode: parallel
  max: 2
  sequence:
    - repeat:
        for_each: "{{ channels }}"
        sequence:
          - if:
              - condition: template
                value_template: "{{ is_state(repeat.item, 'on') }}"
            then:
              - action: switch.turn_off
                continue_on_error: true
                target:
                  entity_id: "{{ repeat.item }}"
              - wait_template: "{{ is_state(repeat.item, 'off') }}"
                timeout: "00:00:15"
                continue_on_timeout: true
              - delay:
                  milliseconds: 250

zelio_all_lights_off:
  alias: All Zelio lights off
  mode: single
  variables:
    relay_a:
      - switch.zelio_a_entry
      - switch.zelio_a_downlights
    relay_b:
      - switch.zelio_b_tracks
  sequence:
    - parallel:
        - sequence:
            - action: script.zelio_off_in_order
              data:
                channels: "{{ relay_a }}"
        - sequence:
            - action: script.zelio_off_in_order
              data:
                channels: "{{ relay_b }}"
    - if:
        - condition: template
          value_template: >-
            {{ (expand(relay_a + relay_b) | selectattr('state', 'eq', 'off') | list | count)
               != ((relay_a + relay_b) | count) }}
      then:
        - stop: "At least one Zelio light did not report OFF"
          error: true
```

## Add the all-off button and export to Alice

If Alice already calls an exported button, keep its entity ID and update
its Home Assistant automation to call the joint script. This preserves
the connection from the existing Alice scene to the button.

Add the button under `input_button:` in `configuration.yaml`. Add the
automation to `automations.yaml` if it is included under `automation:`,
or under the existing `automation:` key in `configuration.yaml`. You can
also create a Button helper in the UI and use its entity ID in the
automation. Press it in Home Assistant first and confirm the script trace
shows all selected switches OFF.
[Button helpers](https://www.home-assistant.io/integrations/input_button/) · [Button pressed trigger](https://www.home-assistant.io/triggers/button.pressed/)

```yaml
input_button:
  zelio_all_lights_off:
    name: All Zelio lights off
    icon: mdi:lightbulb-group-off-outline
```

```yaml
- id: zelio_all_lights_off_button
  alias: Run Zelio lights off from the button
  mode: single
  triggers:
    - trigger: button.pressed
      target:
        entity_id: input_button.zelio_all_lights_off
  actions:
    - action: script.zelio_all_lights_off
```

1. In Home Assistant, open **Settings → Devices & services → Yandex Smart
   Home → Configure**. Choose the UI method for selecting what to share
   and add the reviewed light entities and `input_button.zelio_all_lights_off`.
   Preserve existing exports you still use. If you use a light wrapper,
   select only the wrapper for that load. Give each light a distinct
   friendly name and keep the button's name stable.
   [Choose entities in Yandex Smart Home](https://docs.yaha-cloud.ru/v1.1.x/config/filter/)
2. In **Home with Alice** (Дом с Алисой), link **Yaha Cloud** if needed,
   then refresh the device list. Confirm the individual lights and the
   button appear once. If replacing an earlier export, remove its old
   device in Alice after removing it from the Home Assistant export.
   [Connect and refresh Yaha Cloud](https://docs.yaha-cloud.ru/v1.1.x/platforms/yandex/)
3. Create an Alice scenario such as **All Zelio lights off**. Add a voice
   phrase and choose the exported button's activation as the only device
   action. The button runs the Home Assistant script for both relays.
   Test the scenario and confirm the result in Home Assistant.
   [Create a voice scenario in Alice](https://alice.yandex.ru/support/ru/smart-home/scenarios/voice)
