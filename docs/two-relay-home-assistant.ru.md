# Два реле Zelio в Home Assistant и Алисе

[Read in English](two-relay-home-assistant.md)

Подключите каждое реле Zelio отдельным USB-кабелем SR2USB01 и задайте для
него свой идентификатор устройства в MQTT и свой процесс моста. Оба
процесса могут использовать тот же брокер, к которому подключён Home
Assistant. Соберите CLI и проверьте доступ к USB на компьютере, к которому
подключены кабели.
[Обнаружение реле через USB](discover.ru.md) · [Кабель Schneider Electric SR2USB01](https://www.se.com/uk/en/product/SR2USB01/usb-pc-connecting-cable-for-smart-relay-zelio-logic-3-m/)

## Обнаружение и запуск второго реле

1. Сохраните действующий идентификатор первого реле в MQTT. В примерах
   используется `zelio_a` для первого устройства и новый
   идентификатор `zelio_b` для второго.
2. Подключите второй кабель, откройте на втором реле страницу входов
   и выходов в режиме RUN и выполните `bin/zelio list`. Определите
   серийный номер второго кабеля. Каждому кабелю нужен эксклюзивный доступ
   к USB. Перед обнаружением остановите процессы, использующие второй
   кабель.
3. Подставьте свои значения вместо `SECOND_CABLE_SERIAL` и
   `BROKER_URL`, создайте конфигурацию для чтения и сохраните отчёт локально.

```sh
bin/zelio discover --serial SECOND_CABLE_SERIAL --device-id zelio_b \
  --mqtt-url BROKER_URL --config-out relay-b-readonly.json > relay-b.report.json
bin/zelio validate --config relay-b-readonly.json
bin/zelio monitor-io --serial SECOND_CABLE_SERIAL
```

Сверьте логические выходы второго реле и проверьте каждый кандидат для
света по инструкции проверки одной связи. Проверенная связь для группы
трековых светильников создаёт один MQTT-переключатель со всеми её выходами
`Q`. Используйте его как один канал. В примере выбирается одна проверенная
связь. Подставьте `PROGRAM_SHA256` и `TRACKS_SIGNAL` из отчёта второго
реле. Повторите `--verified-signal` для остальных проверенных связей
с непересекающимися наборами выходов. Для этой настройки выбирайте
только проверенные связи освещения.
[Проверка одной связи](discover.ru.md#проверка-одного-переключателя-перед-внедрением)

```sh
bin/zelio discover --serial SECOND_CABLE_SERIAL --device-id zelio_b \
  --mqtt-url BROKER_URL --expect-program PROGRAM_SHA256 \
  --verified-signal TRACKS_SIGNAL --config-out relay-b.json
bin/zelio validate --config relay-b.json
```

Сохраните конфигурацию первого реле и установите вторую как отдельную
службу. Если шаблон systemd уже установлен, в этом примере первое реле
работает в `zelio@relay_a`, второе в `zelio@relay_b`. Их файлы
конфигурации находятся в `/etc/zelio/relay_a.json` и
`/etc/zelio/relay_b.json`. Если брокер требует учётные данные, задайте
переменные окружения второй службы и предоставьте ей доступ к нужным
темам MQTT. В macOS запускайте каждую конфигурацию в отдельном процессе
`zelio serve`.
[Настройка служб](install.md#linux-systemd) · [Доступ к MQTT](install.md#mqtt-setup)

```sh
sudo install -m 0640 -o root -g zelio relay-b.json /etc/zelio/relay_b.json
sudo systemctl enable --now zelio@relay_b
sudo journalctl -u zelio@relay_b -n 30 --no-pager
```

В Home Assistant откройте **Настройки → Устройства и службы → Объекты**.
Найдите проверенные переключатели обоих реле и используйте их фактические
идентификаторы в примерах ниже. Проверьте каждый переключатель в Home
Assistant перед передачей в Алису. В список освещения включайте только
светильники, оставив вентиляцию и другие нагрузки за его пределами.

## Отображение переключателя как светильника

Можно передать в Алису проверенный `switch.*` напрямую или создать из
него `light.*`, чтобы он отображался в категории освещения. Добавьте
следующую запись в существующий раздел `template:` файла
`configuration.yaml` и подставьте идентификатор исходного переключателя.
Светильник получает состояние и доступность от исходного переключателя.
[Шаблонные светильники Home Assistant](https://www.home-assistant.io/integrations/template/#light)

```yaml
template:
  - light:
      - name: Трековые светильники
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

Для группы треков создайте один светильник из одного обнаруженного
переключателя. Если используете `light.*`, выберите для передачи в Алису
его. Исходный переключатель используйте в скриптах Home Assistant.

## Выключение света на обоих реле

Добавьте оба скрипта в `scripts.yaml`, если он подключён через `script:`,
или под существующий ключ `script:` в `configuration.yaml`. Подставьте в
оба списка идентификаторы проверенных переключателей. Скрипт отправляет
OFF каналам со статусом ON. В каждой ветке он ждёт подтверждённого OFF,
после чего выдерживает паузу перед следующим каналом того же реле. Ветки
для двух реле выполняются параллельно, поскольку у них разные процессы
моста.

По истечении тайм-аута ветка продолжает обработку остальных светильников.
В конце скрипт завершится ошибкой, если хотя бы один переключатель не
подтвердил OFF. Если группа треков недоступна или её выходы имеют разные
состояния, перед повтором проверьте трассировку скрипта и страницу входов
и выходов реле.
[Скрипты Home Assistant](https://www.home-assistant.io/docs/script/) · [Ожидание состояния](https://www.home-assistant.io/building-blocks/wait_template/)

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

## Кнопка общего выключения и передача в Алису

Если сценарий Алисы уже вызывает экспортированную кнопку, сохраните её
идентификатор и направьте автоматизацию Home Assistant на общий скрипт.
В Алисе существующий сценарий продолжит обращаться к той же кнопке.

Добавьте кнопку под ключ `input_button:` в `configuration.yaml`. Добавьте
автоматизацию в `automations.yaml`, если он подключён через `automation:`,
или под существующий ключ `automation:` в `configuration.yaml`. Кнопку
можно создать в интерфейсе в разделе вспомогательных объектов и указать
её идентификатор в автоматизации. Сначала нажмите кнопку в Home Assistant
и по трассировке проверьте, что все выбранные переключатели подтвердили OFF.
[Вспомогательная кнопка](https://www.home-assistant.io/integrations/input_button/) · [Триггер нажатия](https://www.home-assistant.io/triggers/button.pressed/)

```yaml
input_button:
  zelio_all_lights_off:
    name: Выключить весь свет Zelio
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

1. В Home Assistant откройте **Настройки → Устройства и службы → Yandex
   Smart Home → Настроить**. В разделе **Объекты для передачи в УДЯ**
   выберите способ **Через интерфейс** и добавьте проверенные светильники и
   `input_button.zelio_all_lights_off`. Сохраните ранее переданные объекты,
   которыми пользуетесь. Если создали шаблонный светильник, передавайте
   для этой нагрузки только его. Дайте каждому светильнику отдельное
   понятное имя и сохраните имя кнопки.
   [Выбор объектов в Yandex Smart Home](https://docs.yaha-cloud.ru/v1.1.x/config/filter/)
2. В приложении **Дом с Алисой** при необходимости привяжите **Yaha Cloud**
   и обновите список устройств. Убедитесь, что каждый светильник и кнопка
   отображаются в одном экземпляре. При замене ранее переданного объекта
   исключите его из списка в Home Assistant, затем удалите старое
   устройство в Алисе.
   [Подключение и обновление устройств Yaha Cloud](https://docs.yaha-cloud.ru/v1.1.x/platforms/yandex/)
3. Создайте в Алисе сценарий, например **Выключить весь свет Zelio**.
   Добавьте голосовую фразу и выберите срабатывание переданной кнопки
   единственным действием. Она запустит скрипт Home Assistant для обоих
   реле. Запустите сценарий и проверьте результат в Home Assistant.
   [Голосовой сценарий в Алисе](https://alice.yandex.ru/support/ru/smart-home/scenarios/voice)
