# Multi-architecture Harbor tag collision

## Reproduction

Image: `quay.io/cephcsi/cephcsi:v3.14.0`

Parent index: `sha256:d18aa6716ad2559b69d449b0a99cac33a23691bca004706036b29571ec27d1ce`

Arm64 child: `sha256:369bda7b539b35337c12d0151dd3f9128263105dd6d5a1de37a71c156723fe15`

Using the local pre-fix binary and a fresh Harbor:

1. Pull the image without a platform filter.
2. Copy it to Harbor. The tag points to the parent index; children are untagged.
3. Pull the same image with `--platform linux/arm64/v8` under the same tag.
4. Copy the store again.

The store now contains two top-level descriptors with the same reference:

```text
cephcsi/cephcsi:v3.14.0 -> imageIndex d18...
cephcsi/cephcsi:v3.14.0 -> image 369...
```

The pre-fix copy uploads both descriptors using `v3.14.0`. Harbor keeps the last manifest uploaded for a tag, so the tag ends on the arm64 child while the parent index remains untagged.

Repeating the unfiltered pull alone does not create the duplicate child descriptor.

## Root cause

`AddIndex` permits the same reference for different kinds (`imageIndex` and `image`). Registry copy walks both entries and does not resolve the destination-reference collision. Upload order determines which manifest owns the Harbor tag.

The local pre-fix and upstream implementations both have this latent flaw. The upstream `nameMapKey` change alters map layout and made the parent upload last in our upstream runs, so upstream appeared correct. It does not provide an explicit parent-over-child rule.

## Fix

`cmd/hauler/cli/store/copy.go` now collects references owned by image indexes and skips publishing a top-level image when it shares that reference. The child remains part of the parent index and is copied recursively without a separate tag.

The fix is intentionally limited to registry copy. Upstream `store save` behavior is preserved, including Docker archive tags such as `-linux-amd64` and `-linux-arm64-v8`.

## Validation

Regression test: `TestCopyCmd_Registry_PrefersParentIndexOverSameTagChild`.

```bash
go test ./cmd/hauler/cli/store -count=1
```

Live Harbor result after the fix:

```text
tags: ["v3.14.0"]
v3.14.0 -> parent multi-arch index sha256:d18...
arm64 child sha256:369... -> present by digest, untagged
```
