// SPDX-FileCopyrightText: 2026 The ucs-exporter authors
//
// SPDX-License-Identifier: GPL-3.0-only

package dn

import "testing"

func TestParse(t *testing.T) {
	cases := []struct{ dn, want string }{
		// Servers and their components.
		{"sys/chassis-1/blade-3", "chassis=1 blade=3"},
		{"sys/rack-unit-12", "rack=12"},
		{"sys/chassis-2/cartridge-1/server-3", "chassis=2 cartridge=1 server_unit=3"},
		{"sys/chassis-1/blade-3/board/power-stats", "chassis=1 blade=3 board=true leaf=power-stats"},
		{"sys/rack-unit-1/board/temp-stats", "rack=1 board=true leaf=temp-stats"},
		{"sys/chassis-1/blade-3/board/cpu-2/env-stats", "chassis=1 blade=3 board=true cpu=2 leaf=env-stats"},
		{"sys/chassis-1/blade-3/board/memarray-1/mem-14/error-stats", "chassis=1 blade=3 board=true memarray=1 dimm=14 leaf=error-stats"},
		{"sys/rack-unit-2/board/memarray-1/mem-3/dimm-env-stats", "rack=2 board=true memarray=1 dimm=3 leaf=dimm-env-stats"},
		{"sys/chassis-1/blade-3/board/storage-SAS-1/disk-2", "chassis=1 blade=3 board=true storage=SAS-1 disk=2"},
		{"sys/rack-unit-1/board/storage-NVME-1/disk-5", "rack=1 board=true storage=NVME-1 disk=5"},
		{"sys/rack-unit-1/psu-2/stats", "rack=1 psu=2 leaf=stats"},
		{"sys/rack-unit-1/fan-module-1-4/fan-2/stats", "rack=1 fan_module=1-4 fan=2 leaf=stats"},
		{"sys/chassis-1/blade-3/mgmt/fw-system", "chassis=1 blade=3 mgmt=true fw=system"},
		{"sys/chassis-1/blade-3/bios/fw-running", "chassis=1 blade=3 fw=running"},

		// Adapters, host interfaces and virtual circuits.
		{"sys/chassis-1/blade-3/adaptor-1", "chassis=1 blade=3 adaptor=1"},
		{"sys/chassis-1/blade-3/adaptor-1/host-eth-2/vnic-stats", "chassis=1 blade=3 adaptor=1 host_eth=2 leaf=vnic-stats"},
		{"sys/rack-unit-4/adaptor-2/host-fc-1/fc-if-event-stats", "rack=4 adaptor=2 host_fc=1 leaf=fc-if-event-stats"},
		{"sys/chassis-1/blade-3/adaptor-1/ext-eth-3/eth-port-err-stats-rx", "chassis=1 blade=3 adaptor=1 ext_eth=3 leaf=eth-port-err-stats-rx"},
		{"sys/chassis-1/blade-3/adaptor-1/mgmt/fw-system", "chassis=1 blade=3 adaptor=1 mgmt=true fw=system"},
		{"sys/chassis-1/blade-3/fabric-A/path-1/vc-1234", "chassis=1 blade=3 locale=A path=1 vc=1234"},
		{"sys/rack-unit-1/fabric-B/vc-701", "rack=1 locale=B vc=701"},

		// Chassis, IOMs and IOM ports.
		{"sys/chassis-1", "chassis=1"},
		{"sys/chassis-1/stats", "chassis=1 leaf=stats"},
		{"sys/chassis-1/psu-3/stats", "chassis=1 psu=3 leaf=stats"},
		{"sys/chassis-1/fan-module-1-2/fan-1/stats", "chassis=1 fan_module=1-2 fan=1 leaf=stats"},
		{"sys/chassis-1/slot-2", "chassis=1 iom=2"},
		{"sys/chassis-1/slot-2/stats", "chassis=1 iom=2 leaf=stats"},
		{"sys/chassis-1/slot-1/host/port-5/rx-stats", "chassis=1 iom=1 group=host port=5 leaf=rx-stats"},
		{"sys/chassis-1/slot-1/fabric/port-2/ni-err-stats", "chassis=1 iom=1 group=fabric port=2 leaf=ni-err-stats"},
		{"sys/chassis-1/slot-1/fabric/aggr-port-1/port-3", "chassis=1 iom=1 group=fabric aggr=1 port=3"},
		{"sys/chassis-1/slot-1/mgmt/fw-system", "chassis=1 iom=1 mgmt=true fw=system"},
		{"sys/fex-101/slot-1/host/port-12/err-stats", "fex=101 iom=1 group=host port=12 leaf=err-stats"},
		{"sys/fex-101/psu-1", "fex=101 psu=1"},
		{"sys/fex-101/fan-1", "fex=101 fan=1"},

		// Fabric interconnects.
		{"sys/switch-A", "switch=A"},
		{"sys/switch-A/sysstats", "switch=A leaf=sysstats"},
		{"sys/switch-B/envstats", "switch=B leaf=envstats"},
		{"sys/switch-A/slot-1/switch-ether/port-17/err-stats", "switch=A fi_slot=1 group=switch-ether port=17 leaf=err-stats"},
		{"sys/switch-A/slot-1/switch-ether/aggr-port-49/port-2/rx-stats", "switch=A fi_slot=1 group=switch-ether aggr=49 port=2 leaf=rx-stats"},
		{"sys/switch-B/slot-1/switch-fc/port-3/err-stats", "switch=B fi_slot=1 group=switch-fc port=3 leaf=err-stats"},
		{"sys/switch-A/fan-module-1-1/fan-1/stats", "switch=A fan_module=1-1 fan=1 leaf=stats"},
		{"sys/switch-A/fan-2", "switch=A fan=2"},
		{"sys/switch-A/psu-1/stats", "switch=A psu=1 leaf=stats"},
		{"sys/switch-A/mgmt/fw-system", "switch=A mgmt=true fw=system"},
		{"sys/mgmt-entity-A", "mgmt_entity=A"},
		{"sys/mgmt/fw-system", "mgmt=true fw=system"},

		// Logical fabric tree.
		{"fabric/lan/A/pc-11", "cloud=lan side=A pc=11"},
		{"fabric/lan/A/pc-11/rx-stats", "cloud=lan side=A pc=11 leaf=rx-stats"},
		{"fabric/lan/B/pc-11/ep-slot-1-port-17", "cloud=lan side=B pc=11 ep_kind=ep ep_slot=1 ep_port=17"},
		{"fabric/lan/A/phys-slot-1-port-18", "cloud=lan side=A ep_kind=phys ep_slot=1 ep_port=18"},
		{"fabric/lan/A/slot-1-aggr-port-49/phys-slot-1-port-2", "cloud=lan side=A sub_slot=1 sub_aggr=49 ep_kind=phys ep_slot=1 ep_port=2"},
		{"fabric/lan/A/pc-12/slot-1-aggr-port-50/ep-slot-1-port-1", "cloud=lan side=A pc=12 sub_slot=1 sub_aggr=50 ep_kind=ep ep_slot=1 ep_port=1"},
		{"fabric/san/A/pc-1/err-stats", "cloud=san side=A pc=1 leaf=err-stats"},
		{"fabric/san/B/pc-2/ep-slot-1-port-1", "cloud=san side=B pc=2 ep_kind=ep ep_slot=1 ep_port=1"},
		{"fabric/san/A/phys-slot-1-port-3", "cloud=san side=A ep_kind=phys ep_slot=1 ep_port=3"},
		{"fabric/san/A/phys-fcoesanep-slot-1-port-25/fcoe-interface-stats", "cloud=san side=A ep_kind=fcoe-phys ep_slot=1 ep_port=25 leaf=fcoe-interface-stats"},
		{"fabric/san/A/fcoesanpc-5", "cloud=san side=A fcoe_pc=5"},
		{"fabric/san/A/fcoesanpc-5/fcoesanpcep-slot-1-port-27", "cloud=san side=A fcoe_pc=5 ep_kind=fcoe-ep ep_slot=1 ep_port=27"},
		{"fabric/server/chassis-1/slot-4", "cloud=server srv_chassis=1 srv_slot=4"},
		{"fabric/server/sw-A/slot-1-port-5", "cloud=server side=A ep_kind=phys ep_slot=1 ep_port=5"},
		{"fabric/lan/net-VLAN100", "cloud=lan leaf=net-VLAN100"},

		// Organisations and service profiles.
		{"org-root/ls-esx01", "org=root sp=esx01"},
		{"org-root/org-finance/org-db/ls-db01", "org=root/finance/db sp=db01"},
		{"org-root/ls-[web/01]/ether-eth0", "org=root sp=web/01 vnic=eth0"},
		{"org-root/ls-esx01/ether-eth0/if-VLAN100", "org=root sp=esx01 vnic=eth0 if=VLAN100"},
		{"org-root/ls-esx01/fc-fc0", "org=root sp=esx01 vhba=fc0"},
		{"org-root/ls-esx01/fc-fc0/if-default", "org=root sp=esx01 vhba=fc0 if=default"},
		{"org-root/ls-esx01/fc-node", "org=root sp=esx01 fc_node=true"},
		{"org-root/lan-conn-pol-lcp1/ether-eth0", "org=root leaf=ether-eth0 unknown=lan-conn-pol-lcp1"},

		// Faults and oddities.
		{"sys/chassis-1/blade-3/fault-F0283", "chassis=1 blade=3 fault=F0283"},
		{"org-root/ls-esx01/fault-F0327", "org=root sp=esx01 fault=F0327"},
		{"sys/chassis-x/blade-1", "leaf=blade-1 unknown=chassis-x"},
		{"", ""},
		{"sys", ""},
	}
	for _, c := range cases {
		if got := Parse(c.dn).String(); got != c.want {
			t.Errorf("Parse(%q)\n got  %s\n want %s", c.dn, got, c.want)
		}
	}
}

func TestHelpers(t *testing.T) {
	cases := []struct {
		dn                             string
		server, fiPort, ioPort, fabric string
		container                      string
	}{
		{"sys/chassis-1/blade-3/adaptor-1/host-eth-2", "chassis-1/blade-3", "", "", "", ContainerServer},
		{"sys/rack-unit-7/psu-1", "rack-unit-7", "", "", "", ContainerServer},
		{"sys/chassis-2/cartridge-1/server-3", "chassis-2/cartridge-1/server-3", "", "", "", ContainerServer},
		{"sys/chassis-1/blade-3/fabric-B/path-1/vc-99", "chassis-1/blade-3", "", "", "B", ContainerServer},
		{"sys/switch-A/slot-1/switch-ether/port-17", "", "1/17", "", "A", ContainerFI},
		{"sys/switch-B/slot-1/switch-ether/aggr-port-49/port-2", "", "1/49/2", "", "B", ContainerFI},
		{"fabric/lan/A/phys-slot-1-port-18", "", "1/18", "", "A", ContainerNone},
		{"fabric/lan/A/slot-1-aggr-port-49/phys-slot-1-port-2", "", "1/49/2", "", "A", ContainerNone},
		{"sys/chassis-1/slot-2/host/port-5", "", "", "5", "", ContainerIOM},
		{"sys/chassis-1/slot-2/fabric/aggr-port-1/port-3", "", "", "1/3", "", ContainerIOM},
		{"sys/fex-101/slot-1/host/port-12", "", "", "12", "", ContainerFex},
		{"sys/chassis-1/fan-module-1-1/fan-1", "", "", "", "", ContainerChassis},
		{"sys/switch-A/psu-1", "", "", "", "A", ContainerFI},
	}
	for _, c := range cases {
		d := Parse(c.dn)
		server, _ := d.Server()
		fiPort, _ := d.FIPort()
		ioPort, _ := d.IOPort()
		if server != c.server || fiPort != c.fiPort || ioPort != c.ioPort || d.Fabric() != c.fabric || d.Container() != c.container {
			t.Errorf("%s: server=%q fiPort=%q ioPort=%q fabric=%q container=%q; want %q %q %q %q %q",
				c.dn, server, fiPort, ioPort, d.Fabric(), d.Container(), c.server, c.fiPort, c.ioPort, c.fabric, c.container)
		}
	}
	if d := Parse("org-root/org-a/ls-x"); d.OrgPath() != "root/a" {
		got := d.OrgPath()
		t.Errorf("OrgPath = %q", got)
	}
}

func FuzzParse(f *testing.F) {
	f.Add("sys/chassis-1/blade-3/adaptor-1/host-eth-2/vnic-stats")
	f.Add("fabric/lan/A/slot-1-aggr-port-49/phys-slot-1-port-2")
	f.Add("org-root/ls-[a/b]/ether-x/if-y")
	f.Fuzz(func(t *testing.T, s string) {
		d := Parse(s)
		_ = d.String()
		_, _ = d.Server()
		_, _ = d.FIPort()
		_, _ = d.IOPort()
	})
}
