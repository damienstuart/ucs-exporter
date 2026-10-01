# ucs-exporter

A Prometheus exporter for Cisco UCS Manager (UCSM), written in Go.

It polls one or more UCS domains through the UCSM XML API. The exporter covers:

- **Compute:** servers, CPUs, memory and DIMM errors, local disks.
- **Chassis:** chassis, IO modules, FEXes, power supplies, fans and temperatures.
- **Fabric interconnects:** load, memory, HA cluster state, Ethernet, Fibre Channel and FCoE ports and port channels, including error counters.
- **Virtual layer:**
  - service profiles;
  - their vNICs and vHBAs;
  - the adapter interfaces backing them;
  - the virtual circuits (VIFs) and the uplink each circuit is pinned to.

This is a rewrite of [prometheus-ucs-exporter][mwam] by Marshall Wace, which is a fork of Drew Stinnett's
[original exporter][duke] at Duke University OIT. See [Migrating](#migrating-from-prometheus-ucs-exporter) for the
differences.

[mwam]: https://github.com/marshallwace/prometheus-ucs-exporter
[duke]: https://gitlab.oit.duke.edu/oit-ssi-systems/prometheus-ucs-exporter

## Contents

- [Quick start](#quick-start)
- [How it works](#how-it-works)
- [Configuration](#configuration)
- [HTTP endpoints](#http-endpoints)
- [Metrics](#metrics)
- [Grafana dashboard](#grafana-dashboard)
- [Troubleshooting](#troubleshooting)
- [Migrating from prometheus-ucs-exporter](#migrating-from-prometheus-ucs-exporter)
- [Development](#development)
- [Tested against](#tested-against)
- [License](#license)

## Quick start

1. Install the binary, or build it (or the container image) from a clone:

   ```sh
   go install github.com/damienstuart/ucs-exporter/cmd/ucs-exporter@latest

   git clone https://github.com/damienstuart/ucs-exporter && cd ucs-exporter
   make build                 # bin/ucs-exporter
   make docker                # container image ucs-exporter:<version>
   ```

2. Write a configuration file. [`examples/config.yml`](examples/config.yml) documents every option; this is a minimal one:

   ```yaml
   defaults:
     username: 'ucs-CORP\svc_prometheus'   # or $PROM_UCS_USERNAME
     password_file: /run/secrets/ucs_password   # or $PROM_UCS_PASSWORD
     tls:
       ca_file: /etc/ucs-exporter/ucsm-ca.pem
   domains:
     - name: ucs-lon1.example.com
     - name: ucs-nyc1.example.com
   ```

3. Check the configuration, then start the exporter:

   ```sh
   ucs-exporter --config.file=config.yml check-config
   ucs-exporter --config.file=config.yml          # listens on :3001
   curl 'http://localhost:3001/metrics?domain=ucs-lon1.example.com'
   ```

   With Docker Compose, put the password in `examples/secrets/ucs_password`, edit `examples/compose/config.yml`, and run `docker compose up -d`. The image runs as uid 65532, so the secret file must be readable by that user.

4. Add a scrape job with one target per domain, as in [`examples/prometheus.yml`](examples/prometheus.yml):

   ```yaml
   - job_name: ucs
     scrape_interval: 60s
     static_configs:
       - targets: [ucs-lon1.example.com, ucs-nyc1.example.com]
     relabel_configs:
       - {source_labels: [__address__], target_label: __param_domain}
       - {source_labels: [__param_domain], target_label: instance}
       - {target_label: __address__, replacement: 'ucs-exporter:3001'}
   ```

A read-only UCSM account is enough. The exporter logs a warning if the account has admin privileges.

## How it works

- **One poller per domain.** Each configured domain has its own poller. It logs in once and keeps the session alive with `aaaRefresh`. Every `interval` (default 60s, which matches UCSM's default statistics collection interval) it queries the classes the enabled modules need, one `configResolveClass` request per class. At most `max_concurrent_requests` (default 2) requests run at a time.
- **Scrapes are served from memory.** A poll renders all metrics. `/metrics?domain=X` then serves that snapshot, so scrapes are instant and a slow UCSM never slows Prometheus down. Polls of the same domain never overlap.
- **A failing class does not fail the poll.** If one class fails, for example one that an older UCSM release doesn't know, the others are still exported. The last good data for the failing class is kept for up to `max_data_age` (default 3 × interval).
- **Login failures and cookie expiry are handled:**
  - After a failed login, further logins are suppressed for 1 minute, doubling up to 30 minutes, so a wrong password cannot lock out a directory account.
  - An expired cookie triggers a single re-login.
- **Shutdown logs out** of every session.

## Configuration

Settings in `defaults` apply to every domain and can be overridden per domain. [`examples/config.yml`](examples/config.yml) is the reference.

| Setting | Default | Meaning |
|---|---|---|
| `username` | `$PROM_UCS_USERNAME` | UCSM user. Domain accounts look like `ucs-DOMAIN\user`. |
| `password` / `password_file` | `$PROM_UCS_PASSWORD` | Mutually exclusive at each level. `password_file` is re-read at every login. |
| `interval` | `60s` | Poll interval (minimum 10s). |
| `timeout` | 0.75 × interval | Deadline for a whole poll. |
| `request_timeout` | min(30s, timeout) | Deadline for one class query. |
| `max_concurrent_requests` | `2` | Parallel class queries per domain (1–8). |
| `max_data_age` | 3 × interval | How long to keep serving data for a class whose query fails. `0` disables this. |
| `skip_suspect_stats` | `false` | Leave statistics objects (`*Stats` classes) that UCSM flags as suspect (unreliable) out of the metrics. `ucs_class_suspect_objects{class}` counts the flagged objects either way. |
| `tls` | verify | `ca_file`, `server_name`, `insecure_skip_verify`, `min_version`, `cert_file`/`key_file`. A domain-level `tls` block replaces the default block. |
| `proxy_url` | none | HTTP proxy. Proxy environment variables are ignored. |
| `modules` | all | Modules to run; see [Metrics](#metrics). |
| `module_options` | | `virtual.vlans` (`off` by default, `count` or `full`); `ethernet_extended.iom_host_ports` and `ethernet_extended.adaptor_uplink_errors`; `hardware.firmware_types`. |

- **Domain fields:** each domain has a `name` and an optional `address` (`host`, `host:port` or an `https://` URL; defaults to the name). The name is the value of the `?domain=` parameter and of the `domain` label, and it is matched case-insensitively.
- **Unlisted domains:** with `unlisted_domains.enabled`, requests for domains that aren't in the file start a poller that uses the default settings. This is how the Python exporter worked. It is off by default, and when enabled it requires an `allow` list of regular expressions. Without one, anyone who can reach the exporter could make it send the default credentials to a host of their choosing.

**Flags:**

| Flag | Meaning |
|---|---|
| `--config.file` | Configuration file (default `/etc/ucs-exporter/config.yml`). |
| `--web.listen-address` | Listen address (default `:3001`). |
| `--web.config.file` | exporter-toolkit web config, for TLS and basic auth on the exporter itself. |
| `--web.enable-lifecycle` | Enables `POST /-/reload`. |
| `--log.level`, `--log.format` | Logging. |

Every flag can also be set with an environment variable, e.g. `UCS_EXPORTER_CONFIG_FILE`.

**Reloading:** send `SIGHUP` or `POST /-/reload` to reload the configuration. Only domains whose settings changed are restarted, and scrapes are not blocked while their old sessions log out. An invalid file is rejected, the running configuration is kept, and `ucs_exporter_config_last_reload_successful` is set to 0.

## HTTP endpoints

| Path | |
|---|---|
| `/metrics?domain=X` | Metrics for domain X plus its health metrics. |
| `/metrics` | The exporter's own metrics: Go runtime, build info, reloads, domain counts. |
| `/status` | HTML poll status per domain, including failing classes. |
| `/healthz`, `/-/healthy` | Liveness. |
| `/-/ready` | 200 once every configured domain has completed its first poll. |
| `/-/reload` | Reload the configuration; POST only, needs `--web.enable-lifecycle`. |

Responses from `/metrics?domain=X`:

| Response | When |
|---|---|
| 400 | The `domain` parameter is missing or repeated. |
| 404 | The domain is unknown or not allowed. |
| 503 | The domain's first poll has not finished within the scrape timeout (at most 10s). The response includes a `Retry-After` header. |
| 200 | The domain has been polled. If UCSM is unreachable or the login fails, the response contains `ucs_up 0` rather than an error. |

## Metrics

**Conventions:**

- **Names and units:** every metric starts with `ucs_`. Values use base units (bytes, seconds, celsius, watts, bytes per second).
- **Counters:** counters come from UCSM's cumulative statistics, never the per-interval `*Delta` values, and end in `_total`. Use `rate()` and `increase()` on them.
- **Labels:** every series has a `domain` label. Identity labels come from the object's DN and are never empty:

  | Label | Examples |
  |---|---|
  | `server` | `chassis-1/blade-3`, `rack-unit-7` |
  | `chassis`, `iom`, `fex` | |
  | `fabric` | `A`, `B` |
  | `port` | FI: `1/17`, or `1/49/2` for a breakout port. IOM/FEX: the port number. |
  | `port_channel`, `adaptor`, `interface` | |
  | `org`, `service_profile`, `vnic`, `vhba`, `vif` | `org` is a path such as `root/finance` |
- **States:** a UCSM enumeration becomes one series with the raw value as a label, e.g. `ucs_vif_oper_state{…,state="active"} 1`.
- **Health gauges:** each entity has one derived 0/1 gauge, `_up` for connectivity or `_ok` for operability, for easy alerting.
- **Descriptive attributes** live in `_info` metrics with value 1. Examples are model, serial, MAC or WWPN, pinned uplink, and firmware version.

**Modules.** Each module can be enabled or disabled separately. [docs/metrics.md](docs/metrics.md) lists every metric; regenerate it with `ucs-exporter modules --markdown`.

| Module | Covers |
|---|---|
| `system` | Domain name, cluster mode, UCSM version, uptime |
| `capacity` | Servers, CPUs, cores, memory, blade slot usage, server ↔ service profile |
| `power` | Server power, current, voltage |
| `cpu_temperature` | CPU temperatures |
| `fans` | Fan speeds (chassis, rack servers, FEX, FI) |
| `fi_system` | FI load, memory, kernel memory, uptime |
| `memory_errors` | Per-DIMM ECC, parity and CRC error counters |
| `faults` | Active faults by severity and type |
| `ethernet` | Bytes, packets and errors for FI ports, LAN port channels, IOM/FEX host ports |
| `fc` | Bytes and frames for FI FC ports and SAN port channels |
| `vnic` | Per-vNIC and per-vHBA traffic, drops and errors, with service profile labels |
| `virtual` | Service profile state; vNIC/vHBA inventory (MAC, WWPN/WWNN, VLANs, VSAN); adapter host interface link state; virtual circuit state and pinning; adapters and their uplinks |
| `fc_extended` | See below |
| `ethernet_extended` | See below |
| `hardware` | See below |

What the three larger modules cover:

- **`fc_extended`:**
  - FI FC error counters: CRC, discards, link failures, signal and sync losses, too long/short.
  - FC, FCoE and SAN port-channel state and speed, and port-channel members.
  - Adapter-side vHBA counters: link failures, loss of signal/sync, invalid CRC, LIP/NOS, bad frames, FCP requests and bytes. UCSM only reports these for some adapters: on the VIC 1300 and 1400 series adapters tested (UCSM 4.2) the classes are empty, so FC errors come from the FI side (`fcErrStats`). vHBA traffic always comes from the `vnic` module.
- **`ethernet_extended`:**
  - Unicast, multicast, broadcast and jumbo packet counters, plus loss and pause counters.
  - IOM/FEX fabric-link errors.
  - Port and port-channel state and speed.
  - Chassis ↔ FI and adapter ↔ IOM topology.
  - VLAN port-count usage and limit.
- **`hardware`:**
  - State and inventory of servers, CPUs, DIMMs, disks, chassis, IOMs, FEXes and FIs.
  - FI HA cluster state (leadership, HA ready, umbilical).
  - PSUs: state, power, voltage, current and temperature.
  - Fans and fan modules, board and IOM temperatures.
  - Running firmware versions.

**Example queries:**

```promql
# Virtual circuits that are down, with the service profile and vNIC they carry
ucs_vif_up == 0

# Which FI uplink each vNIC of a service profile is pinned to
ucs_vif_pinned_port_info{service_profile="esx01"}

# FC CRC errors in the last hour, FI side and adapter (vHBA) side
increase(ucs_fi_fc_port_crc_errors_total[1h]) > 0
increase(ucs_vhba_crc_errors_total[1h]) > 0

# vNIC throughput per service profile, bits per second
8 * sum by (domain, service_profile) (rate(ucs_vnic_receive_bytes_total[5m]))

# Anything not operable
{__name__=~"ucs_.*_operability_state", state!="operable"}

# Fabric interconnect cluster not ready for failover
ucs_fi_mgmt_ha_ready == 0
```

**Exporter health metrics.** These are served with each domain and carry its `domain` label:

- **Poll:** `ucs_up`, `ucs_poll_duration_seconds`, `ucs_poll_timestamp_seconds`, `ucs_poll_last_success_timestamp_seconds`, `ucs_snapshot_age_seconds`, `ucs_polls_total{result}`, `ucs_poll_overruns_total`.
- **Per class:** `ucs_class_query_success{class}`, `ucs_class_query_duration_seconds{class}`, `ucs_class_query_errors_total{class}`, `ucs_class_objects{class}`, `ucs_class_stale{class}`, `ucs_class_last_success_timestamp_seconds{class}`, and `ucs_class_suspect_objects{class}` for statistics classes.
- **Per module:** `ucs_module_success{module}`, `ucs_module_series{module}`, `ucs_module_skipped_objects{module,reason}`.
- **Session:** `ucs_session_operations_total{op,result}`.

A non-zero `ucs_module_skipped_objects` means the exporter met objects it could not identify, such as an unfamiliar DN layout.

**Series counts and cost.** Measured on a large domain (UCSM 4.2, two 6400-series FIs, about 120 B-series blades with service profiles), with the default options:

| Measure | Value |
|---|---|
| Classes queried | 69 |
| Poll duration | 8–9 s, at the default concurrency of 2 |
| Series | about 75,000 |
| Scrape | about 10 MB (about 400 KB gzipped), served in about 0.1 s |
| Resident memory | 150–230 MB |

The biggest modules there are `memory_errors` (8 counters per DIMM, over 20k series), `hardware` (about 20k), `virtual` (about 10k) and `vnic` (about 7k). Some options add a lot more:

| Option | Cost on that domain |
|---|---|
| `virtual.vlans: count` or `full` | Reads every VLAN-membership object (about 80k there), which adds several seconds per poll. `full` also adds one series per vNIC and VLAN (about 60k there). |
| `ethernet_extended.iom_host_ports` | Packet-type, loss and pause counters for every IOM/FEX host port |

To reduce the count, disable the modules you don't need.

## Grafana dashboard

Import [`grafana/dashboard.json`](grafana/dashboard.json). It shows one domain at a time. There are variables for data source, domain, chassis, server and service profile, and it opens on the last hour. Only the Overview row is expanded initially, so opening the dashboard runs about ten queries; the other rows query when you expand them. The rows are:

1. Overview
2. Faults
3. Capacity
4. Hardware health
5. Power
6. Fabric interconnects
7. LAN uplinks
8. SAN / Fibre Channel
9. Service profiles, vNICs and virtual circuits
10. Chassis backplane
11. Memory errors
12. Firmware
13. Exporter

## Troubleshooting

- **Certificate errors (`x509: …`).** Certificates are verified by default. The Python exporter did not verify them. Choose one:
  - set `tls.ca_file` to the UCSM CA certificate (for a self-signed certificate, the certificate itself);
  - set `tls.server_name` if you connect by IP address;
  - set `tls.insecure_skip_verify: true` for that domain.
- **Old UCSM TLS.** Builds with Go 1.27 or later cannot negotiate RSA key exchange or 3DES cipher suites; Go removed the `tlsrsakex` and `tls3des` GODEBUG settings. A UCSM that offers only those needs a newer UCSM release, or an exporter built with Go 1.26 and run with `GODEBUG=tlsrsakex=1`. `tls.min_version: TLS10` allows TLS 1.0 and 1.1.
- **`login suppressed until …`.** A login failed, for example because of a wrong password or the session limit. Logins back off from 1 minute up to 30 minutes. Fix the credentials and reload, or wait for the backoff to expire.
- **Look at UCSM directly.** The `explore` commands (a Go port of `scripts/explore.py`) use a configured domain (`--domain`) or `--address` with `$PROM_UCS_USERNAME` and `$PROM_UCS_PASSWORD`:

  ```sh
  ucs-exporter explore --domain ucs-lon1.example.com login
  ucs-exporter explore --domain ucs-lon1.example.com query fcErrStats
  ucs-exporter explore --domain ucs-lon1.example.com query faultInst --filter 'severity!=cleared' -o json
  ucs-exporter explore --domain ucs-lon1.example.com dn sys/chassis-1 --hierarchical
  ucs-exporter explore --domain ucs-lon1.example.com children sys/switch-A --class networkElement
  ucs-exporter explore classes      # offline: what every module queries
  ```

  `--debug-xml` prints the XML exchange with passwords and cookies redacted.

## Migrating from prometheus-ucs-exporter

**Behavior changes:**

- **A configuration file is required.** It lists the domains to poll; `PROM_UCS_USERNAME` and `PROM_UCS_PASSWORD` still work as default credentials. Accepting any `?domain=` value, as the Python exporter did, requires `unlisted_domains` with an allowlist.
- **TLS certificates are verified by default.** See [Troubleshooting](#troubleshooting).
- **`/metrics?domain=X` returns only domain X,** from the latest poll. `/metrics` without a domain returns only the exporter's own metrics. Previously every domain's metrics came back on every scrape, one poll behind.
- **The metric names, labels and types changed** (table below). Counters are now counters, not gauges.
- **`ucs_faults` counts active fault instances.** `ucs_faults_total` summed `occur`, including cleared faults.
- **Container:** the default port is still 3001. The container runs as uid 65532 (distroless nonroot), where the old image used 1337.

**Label changes:**

| Old | New |
|---|---|
| `chassis="chassis-1"`, `blade="blade-2"`, `rack="rack-unit-3"`, `"null"` values | `chassis="1"`, `server="chassis-1/blade-2"` or `server="rack-unit-3"` |
| `switch="switch-A"` | `fabric="A"` |
| `pc_label="A"`, `pc_name="pc-11"` | `fabric="A"`, `port_channel="11"` |

**Metric mapping:**

| prometheus-ucs-exporter | ucs-exporter | Notes |
|---|---|---|
| `ucs_servers_total{class}` | `count by (domain, type) (ucs_server_info)` | |
| `ucs_cpus_total`, `ucs_cpu_cores_total` | `ucs_server_cpus`, `ucs_server_cpu_cores` | Per server; `sum` to aggregate |
| `ucs_mem_total`, `ucs_mem_available_total` | `ucs_server_memory_bytes`, `ucs_server_memory_available_bytes` | MB → bytes |
| `ucs_slots_total`, `ucs_slots_equipped`, `ucs_slots_empty` | `ucs_chassis_blade_slots{presence}` | |
| `ucs_compute_mb_consumed_power` | `ucs_server_power_watts` | |
| `ucs_compute_mb_input_current`, `ucs_compute_mb_input_voltage` | `ucs_server_input_current_amperes`, `ucs_server_input_voltage_volts` | |
| `ucs_server_temperature{cpu}` | `ucs_server_cpu_temperature_celsius{cpu}` | The old dashboard queried a non-existent `temperature` metric |
| `ucs_fan_speed` | `ucs_chassis_fan_speed_rpm`, `ucs_server_fan_speed_rpm`, `ucs_fi_fan_speed_rpm`, … | Rack server fans are no longer labelled as a chassis |
| `ucs_cpu_load{switch}` | `ucs_fi_load{fabric}` | |
| `ucs_mem_available`, `ucs_mem_cached` | `ucs_fi_memory_available_bytes`, `ucs_fi_memory_cached_bytes` | MB → bytes |
| `ucs_kernel_mem_total`, `ucs_kernel_mem_free` | `ucs_fi_kernel_memory_total_bytes`, `ucs_fi_kernel_memory_free_bytes` | KiB → bytes (unit to be confirmed) |
| `ucs_ecc_singlebit_errors`, `ucs_ecc_multibit_errors` | `ucs_server_dimm_ecc_singlebit_errors_total`, `…_multibit_errors_total` | Now per DIMM (`memory_array`, `dimm`); previously DIMMs overwrote each other |
| `ucs_address_parity_errors[_correctable\|_un_correctable]` | `ucs_server_dimm_address_parity[_correctable\|_uncorrectable]_errors_total` | |
| `ucs_dram_write_data_[un_]correctable_crc_errors` | `ucs_server_dimm_dram_write_crc_[un]correctable_errors_total` | |
| `ucs_mismatch_errors` | `ucs_server_dimm_mismatch_errors_total` | |
| `ucs_ether_stats_bytes_rx`, `_tx` | `ucs_fi_port_channel_receive_bytes_total`, `…_transmit_bytes_total` | Physical FI ports and IOM/FEX host ports are now included too |
| `ucs_eth_err_*` | `ucs_<entity>_{align_errors,fcs_errors,receive_errors,receive_undersize,receive_internal_mac_errors,transmit_errors,transmit_deferred,transmit_internal_mac_errors,transmit_discards}_total` | Previously IOM ports collided and FI ports collapsed into one series; `align` was never set |
| `ucs_fc_bytes_rx`, `_tx`, `ucs_fc_packets_rx`, `_tx` | `ucs_fi_fc_port_channel_{receive,transmit}_{bytes,frames}_total`, `ucs_fi_fc_port_*` | Previously all four reported bytes received |
| `ucs_vnic_stats_{rx,tx}` | `ucs_vnic_{receive,transmit}_bytes_total`, `ucs_vhba_…` | Per interface with service profile and vNIC/vHBA labels; previously all interfaces of a server overwrote each other |
| `ucs_vnic_stats_packets_{rx,tx}`, `ucs_vnic_stats_errors_{rx,tx}` | `ucs_vnic_{receive,transmit}_{packets,errors}_total`, `ucs_vhba_…` | |
| `ucs_faults_total{type,severity}` | `ucs_faults{severity,type}` | Count of active faults |
| `ucs_exporter_failure` | `ucs_up`, `ucs_polls_total{result}` | |

## Development

```sh
make test        # go vet, gofmt check, then go test -race ./...
make golden      # regenerate testdata/golden after an intended output change, then review the diff
make docs        # regenerate docs/metrics.md
make dashboard   # regenerate grafana/dashboard.json from grafana/generate.py
make run-fake    # serve the synthetic fixtures as a fake UCSM on http://127.0.0.1:8080
```

**Code layout:**

| Path | Contents |
|---|---|
| `internal/ucsm` | XML API client: sessions, streaming decoder with a sanitizer for malformed XML, filters |
| `internal/ucsm/ucsmtest` | A fake UCSM for tests |
| `internal/dn` | DN parser |
| `internal/module` | Module interface, snapshot and rendering |
| `internal/modules` | The metric modules |
| `internal/poller` | Per-domain polling and the manager |
| `internal/server` | HTTP |
| `cmd/ucs-exporter` | CLI, including `explore` and `capture` |

**Tests without UCS hardware:**

- `testdata/fixtures/synthetic` is a small, coherent synthetic domain. The golden files hold the expected output of every module.
- Tests check:
  - that the output is the same with and without attribute projection, which catches a module reading an attribute it didn't declare;
  - that every metric passes promlint;
  - that no two modules declare conflicting metrics;
  - that every class and attribute a module queries exists in Cisco's SDK metadata (`testdata/sdkmeta/classes.json`, generated from ucsmsdk 0.9.27 by `testdata/sdkmeta/generate.py`);
  - that every metric the dashboard uses exists.

**Capturing fixtures from a real UCSM.** Once you have access to a UCSM, capture a fixture set so the modules can be checked against real data:

```sh
ucs-exporter explore --domain ucs-lon1.example.com capture --out testdata/fixtures/lon1
go test ./internal/modules -run TestGolden -update   # writes testdata/golden/lon1
```

- **Redaction:** `capture` blanks session cookies and, by default, pseudonymizes serial numbers, UUIDs, MAC, WWN and IP addresses, asset tags and user labels. The mapping is consistent within a capture, so cross-references still match.
- **Names:** `--redact-names` also pseudonymizes organization and service profile names.
- **Review:** always review a capture before committing it.

## Tested against

The modules were built from ucsmsdk metadata and a synthetic fixture set. They were then checked against a large real UCS domain: UCSM 4.2, 6400-series FIs, B-series blades with VIC 1300 and 1400 series adapters. All modules rendered without errors or unidentified objects. That run confirmed:

- **Units:**
  - FI `memAvailable`/`memCached` and `storageLocalDisk.size` are MiB;
  - `fabricFcSanPc.operSpeed` is aggregate Gbps;
  - `fabricEthLanPc.bandwidth` is aggregate Gbps, while its `operSpeed` is the per-member speed.
- **Virtual circuits:**
  - `dcxVc.vnic` is the vNIC or vHBA name;
  - circuits without a name are infrastructure, such as the FCoE underlay of vHBAs;
  - a circuit pinned to a port channel reports `operBorderSlotId=0` with the port-channel ID in `operBorderPortId`.
- **Templates:** `lsServer.operSrcTemplName` is a DN.
- **API behavior:**
  - UCSM rejects filters written as `<ne …></ne>`, so requests use self-closing elements, as ucsmsdk does;
  - an unknown class returns `ERR-xml-parse-error` (594) `no class named …`, which fails only that class.

Still unconfirmed:

- **Kernel memory unit:** `swSystemStats.kernelMem*` is assumed to be KiB; the observed value (just under 2^25) looks capped.
- **Meaning:**
  - `fcErrStats.rx/tx`;
  - `etherErrStats.rcv/xmit`;
  - `etherPauseStats.resets`;
  - `equipmentIOCardStats.temp` (exported as `sensor="temp"`).
- **Other hardware:** rack servers, FEX, FCoE uplinks, and the adapter-side vHBA counters on adapters that report them. None of these existed in the tested domain.

## License

GPL-3.0-only; see [LICENSE](LICENSE). This project is a derivative work of prometheus-ucs-exporter, (c) 2022 Marshall Wace, which is based on work by Drew Stinnett at Duke University OIT. `testdata/sdkmeta/classes.json` is derived from Cisco's ucsmsdk (Apache License 2.0).
