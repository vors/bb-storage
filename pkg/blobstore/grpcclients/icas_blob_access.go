package grpcclients

import (
	"context"

	remoteexecution "github.com/bazelbuild/remote-apis/build/bazel/remote/execution/v2"
	"github.com/buildbarn/bb-storage/pkg/blobstore"
	"github.com/buildbarn/bb-storage/pkg/blobstore/buffer"
	"github.com/buildbarn/bb-storage/pkg/digest"
	"github.com/buildbarn/bb-storage/pkg/proto/icas"

	"google.golang.org/grpc"
)

type icasBlobAccess struct {
	icasClient              icas.IndirectContentAddressableStorageClient
	maximumMessageSizeBytes int
}

// NewICASBlobAccess creates a BlobAccess that relays any requests to a
// gRPC server that implements the icas.IndirectContentAddressableStorage
// service. This is a service that is specific to Buildbarn, used to
// track references to objects stored in external corpora.
func NewICASBlobAccess(client grpc.ClientConnInterface, maximumMessageSizeBytes int) blobstore.BlobAccess {
	return &icasBlobAccess{
		icasClient:              icas.NewIndirectContentAddressableStorageClient(client),
		maximumMessageSizeBytes: maximumMessageSizeBytes,
	}
}

func (ba *icasBlobAccess) Get(ctx context.Context, digest digest.Digest) buffer.Buffer {
	reference, err := ba.icasClient.GetReference(ctx, &icas.GetReferenceRequest{
		InstanceName: digest.GetInstance(),
		Digest:       digest.GetPartialDigest(),
	})
	if err != nil {
		return buffer.NewBufferFromError(err)
	}
	return buffer.NewProtoBufferFromProto(reference, buffer.Irreparable)
}

func (ba *icasBlobAccess) Put(ctx context.Context, digest digest.Digest, b buffer.Buffer) error {
	reference, err := b.ToProto(&icas.Reference{}, ba.maximumMessageSizeBytes)
	if err != nil {
		return err
	}
	// TODO: The ICAS protocol allows us to do batch updates, while
	// BlobAccess has no mechanics for that. We should extend
	// BlobAccess to support that.
	_, err = ba.icasClient.BatchUpdateReferences(ctx, &icas.BatchUpdateReferencesRequest{
		InstanceName: digest.GetInstance(),
		Requests: []*icas.BatchUpdateReferencesRequest_Request{
			{
				Digest:    digest.GetPartialDigest(),
				Reference: reference.(*icas.Reference),
			},
		},
	})
	return err
}

func (ba *icasBlobAccess) FindMissing(ctx context.Context, digests digest.Set) (digest.Set, error) {
	// Partition all digests by instance name, as the
	// FindMissingReferences() RPC can only process digests for a
	// single instance.
	perInstanceDigests := map[string][]*remoteexecution.Digest{}
	for _, digest := range digests.Items() {
		instanceName := digest.GetInstance()
		perInstanceDigests[instanceName] = append(perInstanceDigests[instanceName], digest.GetPartialDigest())
	}

	missingDigests := digest.NewSetBuilder()
	for instanceName, blobDigests := range perInstanceDigests {
		// Call FindMissingReferences() for each instance.
		request := remoteexecution.FindMissingBlobsRequest{
			InstanceName: instanceName,
			BlobDigests:  blobDigests,
		}
		response, err := ba.icasClient.FindMissingReferences(ctx, &request)
		if err != nil {
			return digest.EmptySet, err
		}

		// Convert results back.
		for _, partialDigest := range response.MissingBlobDigests {
			blobDigest, err := digest.NewDigestFromPartialDigest(instanceName, partialDigest)
			if err != nil {
				return digest.EmptySet, err
			}
			missingDigests.Add(blobDigest)
		}
	}
	return missingDigests.Build(), nil
}
