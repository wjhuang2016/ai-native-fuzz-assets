# Draft: S3 multipart writer can complete a partial object after UploadPart fails

Status: current master RED in local TiDB worktree; asset-store validated with local-fix GREEN. Inserted into remote `found_bug` as id1260005 with `status=current-red-green,confirmed=1`.

## User-Visible Symptom

For large S3 writes using the single-writer multipart path (`Storage.Create` with `Concurrency <= 1`), if one part upload succeeds and a later `UploadPart` fails, cleanup/finalization `Close` can still call `CompleteMultipartUpload` with only the successful prefix parts.

Observed current behavior:

```text
writeErr=ai-native mock upload part failed
closeErr=<nil>
completeCalls=1
completedParts=1
```

So a failed write can publish a truncated/prefix-only object in remote S3 state. The caller may still have the earlier `Write` error, but the storage side effect is wrong: failed multipart upload should not be completed.

## Minimal Repro Shape

The local probe uses the existing mock S3 API:

```text
test:    TestAINativeS3StorageCreateUploadPartFailureThenCloseRED
command: go test ./pkg/objstore/s3store -run TestAINativeS3StorageCreateUploadPartFailureThenCloseRED -count=1 -timeout 60s -v
log:     /Users/bba/pc/ai-native-assets/logs/issue48164-refill-current-s3-storage-part-fail-close-red.log
```

Fault schedule:

```text
1. Create multipart upload.
2. Part 1 UploadPart succeeds and returns ETag.
3. Part 2 UploadPart returns injected root error: "ai-native mock upload part failed".
4. Call Close.
5. RED if Close calls CompleteMultipartUpload, returns nil, or fails to preserve the root error.
```

Current RED:

```text
client_test.go:499:
ai-native s3 storage terminal evidence:
  writeErr=ai-native mock upload part failed
  closeErr=<nil>
  completeCalls=1
  completedParts=1
```

## Source Chain

Current source shape:

```text
pkg/objstore/s3store/client.go

multipartWriter.Write:
  UploadPart error is returned to the caller,
  but the writer does not remember failed state.

multipartWriter.Close:
  always calls CompleteMultipartUpload with u.completeParts.
```

User-facing entry:

```text
pkg/objstore/s3like/store.go
  Storage.Create(..., Concurrency <= 1) selects MultipartWriter
  and wraps it in objectio.NewBufferedWriter.
```

Wrapper behavior:

```text
pkg/objstore/objectio/writer.go
  BufferedWriter.Write returns uploadChunk error.
  BufferedWriter.Close later calls the underlying writer Close.
```

## Fix Direction

Local minimal fix:

```text
multipartWriter stores the first UploadPart error.
If failed state exists:
  Write returns the stored error.
  Close calls AbortMultipartUpload.
  Close returns the stored root error.
```

GREEN evidence:

```text
storage entry:
  writeErr=ai-native mock upload part failed
  closeErr=ai-native mock upload part failed
  abortCalls=1

direct multipart writer entry:
  writeErr=ai-native mock upload part failed
  closeErr=ai-native mock upload part failed
  abortCalls=1
```

Logs:

```text
/Users/bba/pc/ai-native-assets/logs/issue48164-refill-current-s3-multipart-part-fail-close-green.log
/Users/bba/pc/ai-native-assets/logs/issue48164-refill-current-s3-multipart-writer-part-fail-close-green.log
/Users/bba/pc/ai-native-assets/logs/issue48164-refill-current-s3-final-narrow-green.log
```

## Scope

Validated:

```text
pkg/objstore/s3store direct MultipartWriter
pkg/objstore/s3like.Storage.Create(..., Concurrency:1, PartSize:5)
```

Likely follow-up:

```text
pkg/objstore/ossstore/client.go has the same Write-fails-without-state / Close-completes shape.
pkg/objstore/s3store/ks3.go has the same shape.
```

These should be separately executed before claiming blast radius.

## Method Lesson

This came from reusing historical issue48164's broad error-identity oracle, but the new bug is not the old pipe race. The useful refinement was:

```text
root error injected
+ terminal path executed
+ remote terminal action observed (Complete vs Abort)
+ final error identity checked
```

For storage writers, final error text alone is too weak. The oracle must also observe the terminal side effect.
