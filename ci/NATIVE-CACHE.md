# Native cache coverage

The native matrix and verified inventory include every unique exported package
on x86 Linux, ARM Linux and ARM Darwin. `default` aliases `niks3` and needs no
second build. Each selected job retains completed dependencies even after a
later failure; only trusted default-branch events can read the private cache or
publish public transfer artifacts through the separate publisher.

Normal pushes use the repository lock. To reproduce an exact consumer which
follows another public nixpkgs pin, dispatch the native workflow on `flakey`
with `package` and an immutable 40-character `nixpkgs_revision`. The selector
updates only that job's checkout using Nix's lock command. It never fetches a
private consumer flake. Package filesets exclude the workflow and lock metadata;
compare actual drv/out identities before claiming consumer cache coverage.

Output checks exercise CLI help, benchmark Python imports, ext4 superblock and
container manifest contents. Precompiled Go test binaries must be executable;
safe test lists are read, while the server binary is not run because its
TestMain starts PostgreSQL and RustFS even for a test-list request. These checks
do not substitute for the separate Go integration tests or signed cold reuse.

Hosted ARM Linux builds use one compiler core and at least 16 GiB of swap. Four
native check jobs previously lost RustFS compiler processes during thin LTO; no
Niks3 test assertion ran in those failures. CI keeps the pinned RustFS recipe
and every native target, reserves swap only on disposable ARM runners after SDK
cleanup, and fails explicitly if the reservation lacks disk space. Other native
platforms retain their compiler settings. This does not broaden private-cache
access for pull requests.

The external-signer VM gives its OpenBao key-import setup a five-minute client
deadline and a ten-minute service bound for the initial RSA wrapping key under
ARM emulation. Runtime signer and server timeouts are unchanged. Failed external
signer startup prints bounded fixture journals; failed K3s rollout prints bounded
pod status, events and current/previous container logs. K3s readiness assertions
and their existing timeout remain unchanged while diagnosing the ARM crash loop.
