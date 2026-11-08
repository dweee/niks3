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
