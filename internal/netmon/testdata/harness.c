// Sanitizer harness for netmon_darwin.c. Build with TSan or ASan+UBSan
// via the Makefile in this directory.
//
// The harness stubs the Go-exported callback and calls roam_netmon_start
// twice. This exercises two properties:
//
//   - Idempotency: the second call must not start a second monitor set.
//     A second set races on the shared static buffers (TSan reports it)
//     and emits duplicate reports (the count check below catches it even
//     without TSan).
//   - The live path: the monitors deliver a real initial report through
//     fingerprint_path and emit, so the sanitizers see the full code path.
#include <stdatomic.h>
#include <stdio.h>
#include <unistd.h>

#include "netmon_darwin.h"

static _Atomic int reports;

void netmonPathUpdate(int satisfied, char *fingerprint) {
	atomic_fetch_add(&reports, 1);
	printf("report satisfied=%d fp=%s\n", satisfied, fingerprint);
	fflush(stdout);
}

int main(void) {
	roam_netmon_start();
	roam_netmon_start();

	// Wait up to 4 s for the initial report, then let follow-ups land.
	for (int i = 0; i < 40 && atomic_load(&reports) == 0; i++)
		usleep(100000);
	sleep(2);

	int n = atomic_load(&reports);
	if (n == 1) {
		printf("OK: 1 combined report\n");
		return 0;
	}
	if (n == 0) {
		fprintf(stderr, "FAIL: no path report within the wait window\n");
		return 1;
	}
	fprintf(stderr,
	        "FAIL: %d reports, expected 1. Duplicates suggest "
	        "roam_netmon_start started two monitor sets. If the "
	        "network changed during the run, rerun the test.\n",
	        n);
	return 1;
}
