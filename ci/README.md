# Fork Niks3 cache

The native cache workflows build this fork’s client, uploader hook and server on
ARM Darwin, ARM Linux and x86 Linux, plus its Docker image on both Linux targets.
`nix/platforms.json` is the explicit native matrix. `ci/channels.json` supplies
shared helper policy; this change does not create tested/stable channel refs.

Completed selected dependencies are retained after later build failures and
exported as bounded public data (8,192 roots, 1 MiB manifest, 8 GiB stream).
Separate Linux publishers bootstrap the pinned client and publish with GitHub
OIDC and S3 integrity verification. Package builds receive no writer credential.
Trusted default-branch jobs configure refreshing read-only Niks3 access before
bootstrap; PR events receive no private reader or publisher.

The shared helpers match the controller migration used by the other owning
forks; compatibility filenames containing Attic do not enable Attic writes.
Set DAVE_FORK_CI_ENABLED=true and NIKS3_PUSH_ENABLED=true to enable the native
workflow and trusted publication. The backend reader rollout must be applied
before default-branch authenticated reuse is verified.

The existing GoReleaser and container build jobs configure the same read-only
cache. Container builds retain completed graph outputs and export bounded data
to the separate reusable publisher; its writer is never available to image
builds or registry publication. These paths require trusted-event CI proof
after the backend reader rollout. Registry images and release archives retain
the upstream publishing behavior.

The Dependabot vendor-hash job runs on a PR, so it receives no private cache
reader or publisher. Its Go/shell tools now resolve from the repository’s locked
nixpkgs input. Intentional fixed-output hash mismatches are not valid store
outputs; corrected hashes are subsequently built and retained by native CI.

Every separately exported native check and development shell is included by the
matrix planner. `package-*` checks are exact package aliases and share their
package job; `devShell-*` checks cover the corresponding shell. Unknown package
or platform declarations fail planning. Builds request every named output and
artifact names include the output set, so a check cannot overwrite a package.
Linux integration tests run on both architectures: x86 uses KVM, while ARM uses
the supported QEMU TCG fallback with the same test assertions. The existing
bounded build/export/publisher path retains completed dependencies on failure;
a failed required check still fails the native result.

The development shell uses AWS CLI v2. The v1 recipe can reject a consuming
nixpkgs selection whose botocore/s3transfer versions differ; dependency checks
remain enabled. Unit-service fixtures own their database pools through test
cleanup and give database setup and serialized bucket creation separate budgets.
Each compiled Go test binary has a timeout of 20 minutes; proxy request/deadline
assertions remain unchanged. Narrow lint annotations keep intentional channel
error draining and sequential `t.Chdir` tests valid with both supported linter
versions, without disabling those linters globally.
