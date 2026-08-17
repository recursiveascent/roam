#ifndef ROAM_NETMON_H
#define ROAM_NETMON_H

// Starts the default-path monitor on a private dispatch queue. Each update
// calls the Go-exported netmonPathUpdate with a complete snapshot.
void roam_netmon_start(void);

#endif
