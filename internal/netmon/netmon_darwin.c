#include <Network/Network.h>
#include <dispatch/dispatch.h>
#include <stdio.h>
#include <string.h>

#include "netmon_darwin.h"

// Exported from Go (netmon_darwin.go).
extern void netmonPathUpdate(int satisfied, char *fingerprint);

void roam_netmon_start(void) {
	nw_path_monitor_t monitor = nw_path_monitor_create();
	dispatch_queue_t queue =
	    dispatch_queue_create("roam.netmon", DISPATCH_QUEUE_SERIAL);
	nw_path_monitor_set_queue(monitor, queue);
	nw_path_monitor_set_update_handler(monitor, ^(nw_path_t path) {
		int satisfied =
		    nw_path_get_status(path) == nw_path_status_satisfied;
		// One snapshot: interfaces in priority order, name and type;
		// addresses are deliberately excluded from the fingerprint.
		char fpbuf[256] = {0};
		char *fp = fpbuf;
		nw_path_enumerate_interfaces(path, ^bool(nw_interface_t ifc) {
			char part[80];
			snprintf(part, sizeof part, "%s/%d,",
			         nw_interface_get_name(ifc),
			         (int)nw_interface_get_type(ifc));
			strlcat(fp, part, sizeof fp);
			return true;
		});
		netmonPathUpdate(satisfied, fp);
	});
	nw_path_monitor_start(monitor);
}
