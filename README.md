# coffee-ctl

## Config example

Name it `config.yaml` and put in exeuctable directory.

```yaml
log:
  logLevel: info
  timeFormat: 2006-01-02T15:04:05.999999999Z07:00
  logJSON: false

coffee_controllers:
  - name: Example
    api_root: example
    shelly_plug_s_id: A411DF
    shelly_button1_id: 3C6105E51C74
    defaultCountdownNs: 7200000000
```
