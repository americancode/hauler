- [x] Tests added for the regression.
- [x] Focused and full store CLI tests pass.
- [x] Live Harbor reproduction verified.

## Summary

Fixes a multi-architecture image tag collision during registry copy.

After a full multi-platform image is pulled, a later platform-filtered pull can leave both the parent index and a platform child under the same stored reference. A subsequent `hauler store copy` previously published both descriptors using the same registry tag. Harbor retained whichever manifest was uploaded last, which could leave the tag pointing to the arm64 image instead of the parent index.

## Changes

- Detect references owned by a parent image index before registry copy.
- Skip publishing a top-level child image when the same reference belongs to an image index.
- Preserve the child manifest inside the parent multi-platform index.
- Preserve upstream Docker archive behavior, including architecture-specific archive tags.

## Validation

Regression test:

```bash
go test ./cmd/hauler/cli/store -run TestCopyCmd_Registry_PrefersParentIndexOverSameTagChild -count=1
```

Full package test:

```bash
go test ./cmd/hauler/cli/store -count=1
```

Live Harbor validation confirms that the image tag resolves to the parent multi-platform index and that the architecture child remains untagged.
