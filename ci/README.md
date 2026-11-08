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
