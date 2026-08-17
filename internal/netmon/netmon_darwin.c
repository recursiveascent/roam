#include <Network/Network.h>
#include <dispatch/dispatch.h>
#include <stdio.h>
#include <string.h>

#include "netmon_darwin.h"

// Exported from Go (netmon_darwin.go).
extern void netmonPathUpdate(int satisfied, char *fingerprint);

// All state below is mutated only on the serial queue.
static dispatch_queue_t queue;
static char default_fp[128];
static char wifi_fp[64];
static char wired_fp[64];
static int default_satisfied;
static int default_physical; // default path includes a physical interface
static int seen;             // bitmask of monitors that have reported

enum { seen_default = 1, seen_wifi = 2, seen_wired = 4, seen_all = 7 };

// fingerprint_path writes the path's ordered interface list (name/type)
// into buf and reports whether any interface is physical (wifi, wired,
// cellular) rather than a tunnel. Addresses are deliberately excluded.
static int fingerprint_path(nw_path_t path, char *buf, size_t n) {
	buf[0] = 0;
	__block int physical = 0;
	nw_path_enumerate_interfaces(path, ^bool(nw_interface_t ifc) {
		nw_interface_type_t t = nw_interface_get_type(ifc);
		if (t == nw_interface_type_wifi || t == nw_interface_type_wired ||
		    t == nw_interface_type_cellular)
			physical = 1;
		char part[80];
		snprintf(part, sizeof part, "%s/%d,",
		         nw_interface_get_name(ifc), (int)t);
		strlcat(buf, part, n);
		return true;
	});
	return physical;
}

// emit sends one combined report, but only after every monitor has
// delivered its initial state: partial startup snapshots must not look
// like migrations. When the default path runs entirely over tunnels
// (full-tunnel VPN, Tailscale exit node), the wifi and wired underlay
// sections join the fingerprint so physical migrations stay visible;
// otherwise the default path alone identifies the underlay and the
// sections are omitted, so unrelated interface chatter (wifi joining
// while ethernet carries the path) does not trigger reconnects.
static void emit(void) {
	if (seen != seen_all)
		return;
	char combined[288];
	if (default_physical)
		snprintf(combined, sizeof combined, "d:%s", default_fp);
	else
		snprintf(combined, sizeof combined, "d:%s|w:%s|e:%s",
		         default_fp, wifi_fp, wired_fp);
	netmonPathUpdate(default_satisfied, combined);
}

static void watch_default(void) {
	nw_path_monitor_t m = nw_path_monitor_create();
	nw_path_monitor_set_queue(m, queue);
	nw_path_monitor_set_update_handler(m, ^(nw_path_t path) {
		default_satisfied =
		    nw_path_get_status(path) == nw_path_status_satisfied;
		default_physical =
		    fingerprint_path(path, default_fp, sizeof default_fp);
		seen |= seen_default;
		emit();
	});
	nw_path_monitor_start(m);
}

static void watch_underlay(nw_interface_type_t type, char *fp, size_t n,
                           int bit) {
	nw_path_monitor_t m = nw_path_monitor_create_with_type(type);
	nw_path_monitor_set_queue(m, queue);
	nw_path_monitor_set_update_handler(m, ^(nw_path_t path) {
		if (nw_path_get_status(path) == nw_path_status_satisfied)
			fingerprint_path(path, fp, n);
		else
			fp[0] = 0;
		seen |= bit;
		emit();
	});
	nw_path_monitor_start(m);
}

void roam_netmon_start(void) {
	static dispatch_once_t once;
	dispatch_once(&once, ^{
		// The monitors and queue live for the process lifetime and are
		// deliberately never released.
		queue = dispatch_queue_create("roam.netmon", DISPATCH_QUEUE_SERIAL);
		watch_default();
		watch_underlay(nw_interface_type_wifi, wifi_fp, sizeof wifi_fp,
		               seen_wifi);
		watch_underlay(nw_interface_type_wired, wired_fp, sizeof wired_fp,
		               seen_wired);
	});
}
