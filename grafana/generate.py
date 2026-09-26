#!/usr/bin/env python3
# SPDX-FileCopyrightText: 2026 The ucs-exporter authors
#
# SPDX-License-Identifier: GPL-3.0-only
"""Generate grafana/dashboard.json: python3 grafana/generate.py > grafana/dashboard.json

Edit this script rather than the JSON, then regenerate (make dashboard).

Design notes:
- One domain at a time (single-select variable, exact label match), a 1h default
  range, and every row except Overview collapsed, so that opening the dashboard
  only runs the Overview queries. Collapsed rows query when expanded.
- Each table uses exactly one query. Grafana turns several instant queries into
  "Value #A", "Value #B", ... columns; combining metric families in PromQL
  (name regex or "or") gives one row per series instead.
"""
import json
import sys

UID = "warbjdq9hbjeun"
DS = {"type": "prometheus", "uid": "${datasource}"}
D = 'domain="$domain"'
RI = "$__rate_interval"

# Columns hidden in every table: the domain is fixed by the variable.
HIDDEN = ["Time", "__name__", "job", "instance", "domain"]

panels = []
state = {"y": 0, "x": 0, "rowh": 0, "id": 0, "target": panels}


def nid():
    state["id"] += 1
    return state["id"]


def row(title, collapsed=True):
    if state.get("collapsed"):
        # A collapsed row only takes its header's height.
        state["y"] = state["row_y"] + 1
    elif state["x"] != 0:
        state["y"] += state["rowh"]
    state["row_y"], state["collapsed"] = state["y"], collapsed
    state["x"] = 0
    state["rowh"] = 0
    r = {"type": "row", "title": title, "collapsed": collapsed, "id": nid(),
         "gridPos": {"h": 1, "w": 24, "x": 0, "y": state["y"]}, "panels": []}
    panels.append(r)
    state["y"] += 1
    # Panels of a collapsed row live inside the row; Grafana lays them out
    # below the row header when it is expanded.
    state["target"] = r["panels"] if collapsed else panels


def place(w, h):
    if state["x"] + w > 24:
        state["y"] += state["rowh"]
        state["x"] = 0
        state["rowh"] = 0
    pos = {"h": h, "w": w, "x": state["x"], "y": state["y"]}
    state["x"] += w
    state["rowh"] = max(state["rowh"], h)
    return pos


def add(panel):
    state["target"].append(panel)


def targets(queries, table=False):
    out = []
    for i, q in enumerate(queries):
        expr, legend = q if isinstance(q, tuple) else (q, "")
        t = {"datasource": DS, "expr": expr, "refId": chr(65 + i), "legendFormat": legend or "__auto"}
        if table:
            t.update({"format": "table", "instant": True, "range": False})
        out.append(t)
    return out


def timeseries(title, queries, unit="short", w=12, h=8, desc="", stack=False, negative_tx=False):
    fc = {"defaults": {"unit": unit, "custom": {"lineWidth": 1, "fillOpacity": 10, "showPoints": "never",
                                                "stacking": {"mode": "normal" if stack else "none"}}},
          "overrides": []}
    if negative_tx:
        fc["overrides"].append({"matcher": {"id": "byRegexp", "options": ".*(tx|transmit).*"},
                                "properties": [{"id": "custom.transform", "value": "negative-Y"}]})
    add({"type": "timeseries", "title": title, "description": desc, "id": nid(), "datasource": DS,
         "gridPos": place(w, h), "targets": targets(queries), "fieldConfig": fc,
         "options": {"legend": {"displayMode": "table", "placement": "right", "showLegend": True,
                                "calcs": ["lastNotNull", "max"]},
                     "tooltip": {"mode": "multi", "sort": "desc"}}})


def stat(title, query, w=3, h=4, unit="short", red_at=1, desc="", mappings=None, invert=False, neutral=False):
    steps = [{"color": "green", "value": None}, {"color": "red", "value": red_at}]
    if invert:
        steps = [{"color": "red", "value": None}, {"color": "green", "value": red_at}]
    if neutral:
        steps = [{"color": "blue", "value": None}]
    add({"type": "stat", "title": title, "description": desc, "id": nid(), "datasource": DS,
         "gridPos": place(w, h), "targets": targets([query]),
         "fieldConfig": {"defaults": {"unit": unit, "mappings": mappings or [],
                                      "thresholds": {"mode": "absolute", "steps": steps}},
                         "overrides": []},
         "options": {"reduceOptions": {"calcs": ["lastNotNull"], "fields": "", "values": False},
                     "colorMode": "background", "graphMode": "none", "textMode": "auto",
                     "justifyMode": "auto", "orientation": "auto"}})


def table(title, query, w=12, h=8, desc="", value=None, component=None, order=()):
    """A table from one instant query.

    value names the Value column when it carries information (counts);
    otherwise it is hidden. component, for tables combining several metric
    families, is a regex with one group applied to the metric name to make a
    short "component" column (e.g. ucs_(.+)_operability_state gives
    chassis_psu); it comes first, followed by state. order lists further
    columns to put first."""
    hidden = list(HIDDEN)
    rename = {}
    if value:
        rename["Value"] = value
    else:
        hidden.append("Value")
    first = list(order)
    if component:
        query = f'label_replace({query}, "component", "$1", "__name__", "{component}")'
        first = ["component", "state"] + first
    add({"type": "table", "title": title, "description": desc, "id": nid(), "datasource": DS,
         "gridPos": place(w, h), "targets": targets([query], table=True),
         "transformations": [{"id": "organize", "options": {
             "excludeByName": {k: True for k in hidden}, "renameByName": rename,
             "indexByName": {c: i for i, c in enumerate(first)}}}],
         "fieldConfig": {"defaults": {}, "overrides": []},
         "options": {"showHeader": True, "cellHeight": "sm"}})


def bargauge(title, queries, unit="percentunit", w=12, h=8, maxv=1, desc=""):
    add({"type": "bargauge", "title": title, "description": desc, "id": nid(), "datasource": DS,
         "gridPos": place(w, h), "targets": targets(queries),
         "fieldConfig": {"defaults": {"unit": unit, "min": 0, "max": maxv,
                                      "thresholds": {"mode": "absolute", "steps": [
                                          {"color": "green", "value": None},
                                          {"color": "orange", "value": 0.7 * maxv},
                                          {"color": "red", "value": 0.9 * maxv}]}},
                         "overrides": []},
         "options": {"displayMode": "gradient", "orientation": "horizontal",
                     "reduceOptions": {"calcs": ["lastNotNull"], "fields": "", "values": False}}})


def any_of(*exprs):
    """Combine several selectors into one query with "or"."""
    return " or ".join(exprs)


UPDOWN = [{"type": "value", "options": {"0": {"text": "DOWN", "color": "red"}, "1": {"text": "UP", "color": "green"}}}]
YESNO = [{"type": "value", "options": {"0": {"text": "NO", "color": "red"}, "1": {"text": "YES", "color": "green"}}}]

# --- Overview (expanded) ------------------------------------------------------------
row("Overview", collapsed=False)
stat("Domain up", f'ucs_up{{{D}}}', mappings=UPDOWN, invert=True, desc="0 if the latest poll of the domain failed.")
stat("Critical faults", f'sum(ucs_faults{{{D},severity="critical"}}) or vector(0)')
stat("Major faults", f'sum(ucs_faults{{{D},severity="major"}}) or vector(0)')
stat("Servers not operable", f'count(ucs_server_ok{{{D}}} == 0) or vector(0)')
stat("VIFs down", f'count(ucs_vif_up{{{D}}} == 0) or vector(0)', desc="Virtual circuits (vNIC/vHBA paths) whose link is not up.")
stat("LAN uplinks down", f'count(ucs_fi_port_up{{{D},role="network"}} == 0) or vector(0)')
stat("FC ports down", f'count(ucs_fi_fc_port_oper_state{{{D},state!~"up|link-up|sfp-not-present"}}) or vector(0)', desc="FC ports that are not up, excluding empty SFP cages.")
stat("FI HA ready", f'min(ucs_fi_mgmt_ha_ready{{{D}}})', mappings=YESNO, invert=True)
table("Domain", f'ucs_domain_info{{{D}}}', w=12, h=5)
table("Fabric interconnect cluster roles", f'ucs_fi_mgmt_leadership_state{{{D}}}', w=12, h=5)

# --- Faults ---------------------------------------------------------------------
row("Faults")
timeseries("Active faults by severity", [(f'sum by (severity) (ucs_faults{{{D},severity=~"critical|major|minor|warning"}})', "{{severity}}")], stack=True)
table("Active faults by type", f'sum by (severity, type) (ucs_faults{{{D}}}) > 0', value="Faults")

# --- Capacity ----------------------------------------------------------------------
row("Capacity")
stat("Servers", f'count(ucs_server_info{{{D}}})', w=4, neutral=True)
stat("CPU cores", f'sum(ucs_server_cpu_cores{{{D}}})', w=4, neutral=True)
stat("Memory", f'sum(ucs_server_memory_bytes{{{D}}})', w=4, unit="bytes", neutral=True)
stat("Blade slots used", f'sum(ucs_chassis_blade_slots{{{D},presence="equipped"}}) / sum(ucs_chassis_blade_slots{{{D}}})', w=4, unit="percentunit", red_at=0.95)
stat("Service profiles associated", f'count(ucs_service_profile_assoc_state{{{D},state="associated"}}) or vector(0)', w=4, neutral=True)
stat("Service profiles not associated", f'count(ucs_service_profile_assoc_state{{{D},state!="associated"}}) or vector(0)', w=4, neutral=True)
# One row per server with its service profile; servers without one keep an
# empty service_profile (the "or" branch supplies them).
table("Servers", (
    f'ucs_server_info{{{D},server=~"$server"}} * on (domain, server) group_left (org, service_profile) ('
    f'ucs_server_service_profile_info{{{D}}} or on (domain, server) '
    f'label_replace(ucs_server_info{{{D}}} * 0 + 1, "service_profile", "(none)", "", ""))'), w=24, h=8,
      order=("server", "service_profile", "org", "type", "model", "serial", "vendor"))

# --- Hardware health ---------------------------------------------------------------
row("Hardware health")
table("Components not operable",
      f'{{__name__=~"ucs_.+_operability_state",{D},state!="operable"}}', w=24, h=8,
      component="ucs_(.+)_operability_state",
      desc="Every component whose operability is not 'operable': servers, CPUs, DIMMs, disks, adapters, chassis, IOMs, FEXes, FIs, PSUs, fans and fan modules.")
timeseries("CPU temperature (max per server)", [(f'max by (server) (ucs_server_cpu_temperature_celsius{{{D},server=~"$server"}})', "{{server}}")], unit="celsius", w=8)
timeseries("DIMM temperature (max per server)", [(f'max by (server) (ucs_server_dimm_temperature_celsius{{{D},server=~"$server"}})', "{{server}}")], unit="celsius", w=8)
timeseries("IOM temperatures", [(f'ucs_iom_temperature_celsius{{{D},chassis=~"$chassis"}}', "chassis {{chassis}} IOM {{iom}} {{sensor}}")], unit="celsius", w=8)

# --- Power ------------------------------------------------------------------------
row("Power")
timeseries("Chassis input power", [(f'ucs_chassis_input_power_watts{{{D},chassis=~"$chassis"}}', "chassis {{chassis}}")], unit="watt", w=8)
timeseries("Top 10 servers by power", [(f'topk(10, ucs_server_power_watts{{{D},server=~"$server"}})', "{{server}}")], unit="watt", w=8)
table("Power supplies not OK", f'{{__name__=~"ucs_.+_psu_ok",{D}}} == 0', w=8, component="ucs_(.+)_ok")

# --- Fabric interconnects -------------------------------------------------------------
row("Fabric interconnects")
timeseries("CPU load", [(f'ucs_fi_load{{{D}}}', "{{fabric}}")], w=8)
timeseries("Memory available", [(f'ucs_fi_memory_available_bytes{{{D}}}', "{{fabric}}")], unit="bytes", w=8)
timeseries("Kernel memory used", [(f'1 - ucs_fi_kernel_memory_free_bytes{{{D}}} / ucs_fi_kernel_memory_total_bytes{{{D}}}', "{{fabric}}")], unit="percentunit", w=8)
timeseries("Temperatures", [(f'ucs_fi_temperature_celsius{{{D}}}', "{{fabric}} {{sensor}}")], unit="celsius", w=8)
timeseries("Fan speed", [(f'ucs_fi_fan_speed_rpm{{{D}}}', "{{fabric}} fan {{fan}}")], unit="rotrpm", w=8)
bargauge("VLAN port usage", [(f'ucs_fi_vlan_ports{{{D}}} / ucs_fi_vlan_ports_limit{{{D}}}', "{{fabric}}")], w=8,
         desc="VLAN port instances in use as a fraction of the platform limit.")

# --- LAN ---------------------------------------------------------------------------
row("LAN uplinks")
timeseries("Uplink port channel traffic", [
    (f'8 * rate(ucs_fi_port_channel_receive_bytes_total{{{D}}}[{RI}])', "{{fabric}} Po{{port_channel}} rx"),
    (f'8 * rate(ucs_fi_port_channel_transmit_bytes_total{{{D}}}[{RI}])', "{{fabric}} Po{{port_channel}} tx")], unit="bps", negative_tx=True)
timeseries("Uplink port traffic", [
    (f'8 * rate(ucs_fi_port_receive_bytes_total{{{D},role="network"}}[{RI}])', "{{fabric}} {{port}} rx"),
    (f'8 * rate(ucs_fi_port_transmit_bytes_total{{{D},role="network"}}[{RI}])', "{{fabric}} {{port}} tx")], unit="bps", negative_tx=True)
timeseries("FI port errors", [(f'sum by (fabric, port, role) (rate(ucs_fi_port_fcs_errors_total{{{D}}}[{RI}]) + rate(ucs_fi_port_receive_errors_total{{{D}}}[{RI}]) + rate(ucs_fi_port_transmit_errors_total{{{D}}}[{RI}]) + rate(ucs_fi_port_align_errors_total{{{D}}}[{RI}])) > 0', "{{fabric}} {{port}} ({{role}})")], unit="pps")
timeseries("FI port discards and pause frames", [
    (f'rate(ucs_fi_port_transmit_discards_total{{{D}}}[{RI}]) > 0', "{{fabric}} {{port}} tx discards"),
    (f'rate(ucs_fi_port_receive_pause_frames_total{{{D}}}[{RI}]) > 0', "{{fabric}} {{port}} rx pause"),
    (f'rate(ucs_fi_port_transmit_pause_frames_total{{{D}}}[{RI}]) > 0', "{{fabric}} {{port}} tx pause")], unit="pps")
table("Uplinks and port channels down", any_of(
    f'ucs_fi_port_oper_state{{{D},role="network",state!~"up|link-up"}}',
    f'ucs_fi_port_channel_oper_state{{{D},state!~"up|link-up"}}',
    f'ucs_fi_port_channel_member_state{{{D},state!="up"}}'), w=24, component="ucs_fi_(.+)_state")

# --- SAN ------------------------------------------------------------------------------
row("SAN / Fibre Channel")
timeseries("FC port traffic", [
    (f'8 * rate(ucs_fi_fc_port_receive_bytes_total{{{D}}}[{RI}])', "{{fabric}} fc{{port}} rx"),
    (f'8 * rate(ucs_fi_fc_port_transmit_bytes_total{{{D}}}[{RI}])', "{{fabric}} fc{{port}} tx")], unit="bps", negative_tx=True)
timeseries("FC CRC errors (per hour)", [
    (f'increase(ucs_fi_fc_port_crc_errors_total{{{D}}}[1h]) > 0', "{{fabric}} fc{{port}}"),
    (f'increase(ucs_fi_fc_port_channel_crc_errors_total{{{D}}}[1h]) > 0', "{{fabric}} san-po{{port_channel}}")])
timeseries("FC link failures, signal and sync losses (per hour)", [
    (f'increase(ucs_fi_fc_port_link_failures_total{{{D}}}[1h]) > 0', "{{fabric}} fc{{port}} link failures"),
    (f'increase(ucs_fi_fc_port_signal_losses_total{{{D}}}[1h]) > 0', "{{fabric}} fc{{port}} signal losses"),
    (f'increase(ucs_fi_fc_port_sync_losses_total{{{D}}}[1h]) > 0', "{{fabric}} fc{{port}} sync losses")])
timeseries("vHBA errors (adapter side, per hour)", [
    (f'increase(ucs_vhba_crc_errors_total{{{D},service_profile=~"$service_profile"}}[1h]) > 0', "{{service_profile}} {{vhba}} CRC"),
    (f'increase(ucs_vhba_link_failures_total{{{D},service_profile=~"$service_profile"}}[1h]) > 0', "{{service_profile}} {{vhba}} link failures"),
    (f'increase(ucs_vhba_sync_losses_total{{{D},service_profile=~"$service_profile"}}[1h]) > 0', "{{service_profile}} {{vhba}} sync losses"),
    (f'increase(ucs_vhba_port_receive_bad_frames_total{{{D},service_profile=~"$service_profile"}}[1h]) > 0', "{{service_profile}} {{vhba}} bad frames")],
    desc="Only some adapters report these; see the README.")
table("FC ports, SAN port channels and FCoE uplinks not up", any_of(
    f'ucs_fi_fc_port_oper_state{{{D},state!~"up|link-up|sfp-not-present"}}',
    f'ucs_fi_fc_port_channel_oper_state{{{D},state!~"up|link-up"}}',
    f'ucs_fi_fc_port_channel_member_state{{{D},state!="up"}}',
    f'ucs_fi_fcoe_uplink_oper_state{{{D},state!="up"}}'), w=24, component="ucs_fi_(.+)_state")

# --- Virtual ------------------------------------------------------------------------
row("Service profiles, vNICs and virtual circuits")
table("Service profiles not OK", f'ucs_service_profile_oper_state{{{D},service_profile=~"$service_profile",state!~"ok|unassociated"}}')
table("Virtual circuits down", f'ucs_vif_oper_state{{{D},service_profile=~"$service_profile",state!="active"}}',
      desc="VIFs (one per vNIC/vHBA per fabric) whose operational state is not active.",
      order=("service_profile", "vnic", "fabric", "state", "server", "vif", "transport"))
timeseries("vNIC traffic", [
    (f'8 * sum by (service_profile, vnic) (rate(ucs_vnic_receive_bytes_total{{{D},service_profile=~"$service_profile"}}[{RI}]))', "{{service_profile}} {{vnic}} rx"),
    (f'8 * sum by (service_profile, vnic) (rate(ucs_vnic_transmit_bytes_total{{{D},service_profile=~"$service_profile"}}[{RI}]))', "{{service_profile}} {{vnic}} tx")], unit="bps", negative_tx=True)
timeseries("vHBA traffic", [
    (f'8 * sum by (service_profile, vhba) (rate(ucs_vhba_receive_bytes_total{{{D},service_profile=~"$service_profile"}}[{RI}]))', "{{service_profile}} {{vhba}} rx"),
    (f'8 * sum by (service_profile, vhba) (rate(ucs_vhba_transmit_bytes_total{{{D},service_profile=~"$service_profile"}}[{RI}]))', "{{service_profile}} {{vhba}} tx")], unit="bps", negative_tx=True)
timeseries("vNIC drops and errors", [
    (f'sum by (service_profile, vnic) (rate(ucs_vnic_receive_dropped_total{{{D},service_profile=~"$service_profile"}}[{RI}]) + rate(ucs_vnic_transmit_dropped_total{{{D},service_profile=~"$service_profile"}}[{RI}])) > 0', "{{service_profile}} {{vnic}} drops"),
    (f'sum by (service_profile, vnic) (rate(ucs_vnic_receive_errors_total{{{D},service_profile=~"$service_profile"}}[{RI}]) + rate(ucs_vnic_transmit_errors_total{{{D},service_profile=~"$service_profile"}}[{RI}])) > 0', "{{service_profile}} {{vnic}} errors")], unit="pps")
table("Virtual circuit pinning", any_of(
    f'ucs_vif_pinned_port_info{{{D},service_profile=~"$service_profile"}}',
    f'ucs_vif_pinned_port_channel_info{{{D},service_profile=~"$service_profile"}}'),
      desc="The FI uplink port or port channel each virtual circuit is pinned to.",
      order=("service_profile", "vnic", "fabric", "port", "port_channel", "server", "vif", "transport"))
table("vNICs", f'ucs_vnic_info{{{D},service_profile=~"$service_profile"}}', order=("service_profile", "vnic", "admin_fabric", "mac"))
table("vHBAs", f'ucs_vhba_info{{{D},service_profile=~"$service_profile"}}', order=("service_profile", "vhba", "admin_fabric", "wwpn", "wwnn", "vsan"))

# --- Chassis backplane ----------------------------------------------------------------
row("Chassis backplane")
stat("IOM active fabric ports (min)", f'min(ucs_iom_active_fabric_ports{{{D},chassis=~"$chassis"}})', w=4, neutral=True)
table("IOM host ports down (with connected adapter)", f'(ucs_iom_host_port_up{{{D},chassis=~"$chassis"}} == 0) * on (domain, chassis, iom, port) group_left (server, adaptor, interface) ucs_iom_host_port_peer_info{{{D}}}', w=10)
timeseries("IOM fabric port CRC errors (per hour)", [(f'increase(ucs_iom_fabric_port_crc_errors_total{{{D},chassis=~"$chassis"}}[1h])', "chassis {{chassis}} IOM {{iom}} port {{port}}")], w=10)
table("IOM to FI links", f'ucs_iom_fabric_port_peer_info{{{D},chassis=~"$chassis"}}', w=24, h=6)

# --- Memory errors ------------------------------------------------------------------
row("Memory errors")
timeseries("Correctable ECC errors (top 10 DIMMs, per hour)", [(f'topk(10, increase(ucs_server_dimm_ecc_singlebit_errors_total{{{D},server=~"$server"}}[1h]) > 0)', "{{server}} DIMM {{memory_array}}/{{dimm}}")])
table("DIMMs with uncorrectable errors", f'(ucs_server_dimm_ecc_multibit_errors_total{{{D}}} > 0) * on (domain, server, memory_array, dimm) group_left (location) ucs_server_dimm_info{{{D}}}', value="Errors")

# --- Firmware -------------------------------------------------------------------------
row("Firmware")
table("Running firmware", f'ucs_firmware_running_info{{{D}}}', w=24, h=10)

# --- Exporter -------------------------------------------------------------------------
row("Exporter")
timeseries("Poll duration", [(f'ucs_poll_duration_seconds{{{D}}}', "poll")], unit="s", w=8)
timeseries("Data age", [(f'ucs_snapshot_age_seconds{{{D}}}', "age")], unit="s", w=8)
table("Failing class queries", f'ucs_class_query_success{{{D}}} == 0', w=8)

# --- Variables --------------------------------------------------------------------------
templating = [
    {"type": "datasource", "name": "datasource", "label": "Data source", "query": "prometheus", "current": {}, "hide": 0},
]


def var(name, label, query, multi=True):
    v = {"type": "query", "name": name, "label": label, "datasource": DS,
         "query": {"query": query, "refId": "PrometheusVariableQueryEditor-VariableQuery", "qryType": 1},
         "definition": query, "refresh": 2, "includeAll": multi, "multi": multi,
         "current": {}, "sort": 1, "hide": 0}
    if multi:
        v["allValue"] = ".*"
    return v


templating += [
    var("domain", "Domain", "label_values(ucs_up, domain)", multi=False),
    var("chassis", "Chassis", 'label_values(ucs_chassis_info{domain="$domain"}, chassis)'),
    var("server", "Server", 'label_values(ucs_server_info{domain="$domain"}, server)'),
    var("service_profile", "Service profile", 'label_values(ucs_service_profile_oper_state{domain="$domain"}, service_profile)'),
]

dashboard = {
    "title": "Cisco UCS",
    "uid": UID,
    "description": "A Cisco UCS Manager domain monitored by ucs-exporter.",
    "tags": ["cisco", "ucs", "ucs-exporter"],
    "editable": True,
    "graphTooltip": 1,
    "refresh": "1m",
    "time": {"from": "now-1h", "to": "now"},
    "timezone": "",
    "schemaVersion": 39,
    "version": 1,
    "panels": panels,
    "templating": {"list": templating},
    "annotations": {"list": []},
    "links": [],
}

json.dump(dashboard, sys.stdout, indent=2)
sys.stdout.write("\n")
