# Multi-architecture Harbor tag investigation

## Scope

Image tested: `quay.io/cephcsi/cephcsi:v3.14.0`

Parent index:

`sha256:d18aa6716ad2559b69d449b0a99cac33a23691bca004706036b29571ec27d1ce`

Children:

- amd64: `sha256:df99379b04b4433451ae9d6a34a25620fce5b16f2834e9634d970ca34de04408`
- arm64/v8: `sha256:369bda7b539b35337c12d0151dd3f9128263105dd6d5a1de37a71c156723fe15`

Harbor test endpoint: `localhost:5000`, Harbor 2.15.4, Podman-backed kind cluster `hauler-repro`.

## Definitive reproduction with local pre-fix code

The local pre-fix binary was built from repository `HEAD` (`3af11fc`). No upstream binary was used for this reproduction.

1. Fresh store, unfiltered pull:

   ```text
   cephcsi/cephcsi:v3.14.0 -> parent index d18...
   ```

2. Push 1 with `hauler store copy registry://localhost:5000`:

   Harbor tag `v3.14.0` resolved to the parent index `d18...`; amd64 and arm64 children were untagged.

3. Pull arm64 under the same tag:

   ```text
   hauler store add image quay.io/cephcsi/cephcsi:v3.14.0 \
     --platform linux/arm64/v8
   ```

   The store then contained two top-level entries with the same reference:

   ```text
   cephcsi/cephcsi:v3.14.0 -> imageIndex d18...
   cephcsi/cephcsi:v3.14.0 -> image 369...
   ```

   They coexist because the internal key includes both reference and kind (`imageIndex` versus `image`).

4. Push 2 with the same `store copy` command:

   The pre-fix binary uploaded the parent first and arm64 child second, both as `v3.14.0`.

   Harbor then resolved:

   ```text
   v3.14.0 -> sha256:369... (arm64 child)
   ```

   The parent index remained in Harbor but lost the tag. Its children remained untagged except for the arm64 child tag.

## Unfiltered repeat

Repeating the exact unfiltered add twice did not create the duplicate child reference. The store remained parent-only and the second copy kept Harbor's parent tag.

Therefore, the direct-copy failure requires the store to acquire a same-tag child descriptor. In the tested sequence that happened through a same-tag platform-specific pull.

## Current patch result

The final fix is intentionally limited to registry copy. It preserves upstream `store save` behavior, including architecture-specific Docker archive tags, while preventing those same-reference child descriptors from being published as registry tags.

## Relevant implementation facts

- `pkg/content/oci.go:AddIndex` keys entries by reference plus kind, allowing the same tag to exist once as `imageIndex` and once as `image`.
- `cmd/hauler/cli/store/copy.go` walks every top-level entry and derives the destination from `AnnotationRefName`.
- `pkg/store/store.go:CopyAll`/`Copy` recursively copies the descriptor graph but does not deduplicate conflicting top-level destination references.
- Harbor accepts the uploads; the last manifest PUT for a tag owns that tag.

## Upstream comparison

The upstream checkout `hauler-original/hauler` uses the same reference-plus-kind model and the same copy pattern for this scenario. The local pre-fix and upstream code differ in many unrelated areas, but neither has a guard that prevents a child image from being published under a tag already used by a parent index.

The most relevant delta is `pkg/content/oci.go:nameMapKey`:

- upstream keys digest references with `parsed.Name()`, retaining `repo@sha256:<digest>`;
- the local pre-fix code keys digest references with `key.Context().String()`, retaining only the repository.

That changes the name-map bucket layout and therefore the observed `Walk` iteration order. In the reproduced store, the local pre-fix binary uploaded parent then child and corrupted Harbor. The upstream binary uploaded child then parent and left Harbor correct. This is an ordering difference, not a semantic safety fix: upstream still walks and publishes both same-tag descriptors, and Harbor would still accept whichever one is uploaded last.

The upstream binary was also run repeatedly against the same duplicate-reference store; it consistently happened to leave the parent last in this environment. Source inspection shows no deduplication or parent-over-child rule in upstream `store copy`, so upstream is not guaranteed safe for this state.

The architecture-suffix code is a separate `store save` issue; removing those suffixes does not address this direct OCI-copy collision.

## Required fix direction

Implemented in `cmd/hauler/cli/store/copy.go`: before the registry walk, collect references owned by `dev.hauler/imageIndex`; during the push walk, skip a top-level `dev.hauler/image` descriptor when its reference is also owned by an image index. The index copy recursively transfers the child manifests without assigning them the conflicting tag. Digest references may still be published by digest.

Add a regression test that asserts:

1. Push 1 tags the parent index.
2. A same-tag platform pull creates the duplicate store state.
3. Push 2 leaves the parent index tagged and the child untagged.

The test is `TestCopyCmd_Registry_PrefersParentIndexOverSameTagChild` in `cmd/hauler/cli/store/copy_test.go`. Focused and full copy test suites pass. The fixed binary was also run against the live Harbor duplicate-reference store; Harbor resolved `v3.14.0` to parent digest `d18...` after the copy.

Live Harbor verification also showed:

```text
tags: ["v3.14.0"]
v3.14.0 -> application/vnd.docker.distribution.manifest.list.v2+json
v3.14.0 -> sha256:d18...
arm64 child sha256:369... -> exists by digest, no tag
```

The direct-copy fix intentionally does not retain a separate Harbor tag for the child. The child remains reachable through the parent index and is untagged in Harbor. Docker archive generation retains the upstream `-linux-amd64` and `-linux-arm64-v8` tags.

