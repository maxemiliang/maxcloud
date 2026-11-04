package objectstorage

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/UpCloudLtd/upcloud-go-api/v8/upcloud"
	"github.com/UpCloudLtd/upcloud-go-api/v8/upcloud/client"
	"github.com/UpCloudLtd/upcloud-go-api/v8/upcloud/request"
	"github.com/UpCloudLtd/upcloud-go-api/v8/upcloud/service"
)

// UpCloud implements Storage with UpCloud's API, for the Object Storage
// instance ServiceUUID.
type UpCloud struct {
	svc         *service.Service
	serviceUUID string
}

// NewUpCloud returns a Storage for one Object Storage instance, using an API
// token.
func NewUpCloud(token, serviceUUID string) *UpCloud {
	c := client.New("", "", client.WithBearerAuth(token), client.WithTimeout(60*time.Second))
	return &UpCloud{svc: service.New(c), serviceUUID: serviceUUID}
}

// UpCloud allows at most 5 versions of a policy.
const maxPolicyVersions = 5

func (u *UpCloud) Buckets(ctx context.Context) (map[string]BucketMetrics, error) {
	out := map[string]BucketMetrics{}
	const size = 100
	for page := 1; ; page++ {
		ms, err := u.svc.GetManagedObjectStorageBucketMetrics(ctx, &request.GetManagedObjectStorageBucketMetricsRequest{
			ServiceUUID: u.serviceUUID,
			Page:        &request.Page{Size: size, Number: page},
		})
		if err != nil {
			return nil, fmt.Errorf("list buckets: %w", err)
		}
		for _, m := range ms {
			if m.Deleted {
				continue
			}
			out[m.Name] = BucketMetrics{SizeBytes: int64(m.TotalSizeBytes), Objects: int64(m.TotalObjects)}
		}
		if len(ms) < size {
			return out, nil
		}
	}
}

func (u *UpCloud) EnsureBucket(ctx context.Context, name string) error {
	_, err := u.svc.CreateManagedObjectStorageBucket(ctx, &request.CreateManagedObjectStorageBucketRequest{
		ServiceUUID: u.serviceUUID, Name: name,
	})
	if err != nil && !isConflict(err) {
		return fmt.Errorf("create bucket %s: %w", name, err)
	}
	return nil
}

func (u *UpCloud) DeleteBucket(ctx context.Context, name string) error {
	err := u.svc.DeleteManagedObjectStorageBucket(ctx, &request.DeleteManagedObjectStorageBucketRequest{
		ServiceUUID: u.serviceUUID, Name: name,
	})
	if err != nil && !isNotFound(err) {
		return fmt.Errorf("delete bucket %s: %w", name, err)
	}
	return nil
}

func (u *UpCloud) EnsureUser(ctx context.Context, username string) error {
	_, err := u.svc.GetManagedObjectStorageUser(ctx, &request.GetManagedObjectStorageUserRequest{
		ServiceUUID: u.serviceUUID, Username: username,
	})
	if err == nil {
		return nil
	}
	if !isNotFound(err) {
		return fmt.Errorf("get user %s: %w", username, err)
	}
	_, err = u.svc.CreateManagedObjectStorageUser(ctx, &request.CreateManagedObjectStorageUserRequest{
		ServiceUUID: u.serviceUUID, Username: username,
	})
	if err != nil && !isConflict(err) {
		return fmt.Errorf("create user %s: %w", username, err)
	}
	return nil
}

func (u *UpCloud) DeleteUser(ctx context.Context, username string) error {
	user, err := u.svc.GetManagedObjectStorageUser(ctx, &request.GetManagedObjectStorageUserRequest{
		ServiceUUID: u.serviceUUID, Username: username,
	})
	if isNotFound(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("get user %s: %w", username, err)
	}
	for _, k := range user.AccessKeys {
		if err := u.DeleteAccessKey(ctx, username, k.AccessKeyID); err != nil {
			return err
		}
	}
	for _, p := range user.Policies {
		err := u.svc.DetachManagedObjectStorageUserPolicy(ctx, &request.DetachManagedObjectStorageUserPolicyRequest{
			ServiceUUID: u.serviceUUID, Username: username, Name: p.Name,
		})
		if err != nil && !isNotFound(err) {
			return fmt.Errorf("detach policy %s from %s: %w", p.Name, username, err)
		}
	}
	err = u.svc.DeleteManagedObjectStorageUser(ctx, &request.DeleteManagedObjectStorageUserRequest{
		ServiceUUID: u.serviceUUID, Username: username,
	})
	if err != nil && !isNotFound(err) {
		return fmt.Errorf("delete user %s: %w", username, err)
	}
	return nil
}

func (u *UpCloud) EnsurePolicy(ctx context.Context, name, description, document string) error {
	p, err := u.svc.GetManagedObjectStoragePolicy(ctx, &request.GetManagedObjectStoragePolicyRequest{
		ServiceUUID: u.serviceUUID, Name: name,
	})
	if isNotFound(err) {
		_, err = u.svc.CreateManagedObjectStoragePolicy(ctx, &request.CreateManagedObjectStoragePolicyRequest{
			ServiceUUID: u.serviceUUID, Name: name, Description: description, Document: document,
		})
		if err != nil && !isConflict(err) {
			return fmt.Errorf("create policy %s: %w", name, err)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("get policy %s: %w", name, err)
	}
	if same, err := sameDocument(p.Document, document); err != nil || same {
		return err
	}

	// A new default version. Make room first: the oldest versions that
	// aren't the default go.
	versions, err := u.svc.GetManagedObjectStoragePolicyVersions(ctx, &request.GetManagedObjectStoragePolicyVersionsRequest{
		ServiceUUID: u.serviceUUID, Name: name,
	})
	if err != nil {
		return fmt.Errorf("list versions of policy %s: %w", name, err)
	}
	slices.SortFunc(versions, func(a, b upcloud.ManagedObjectStoragePolicyVersion) int {
		return a.CreatedAt.Compare(b.CreatedAt)
	})
	left := len(versions)
	for _, v := range versions {
		if left < maxPolicyVersions {
			break
		}
		if v.IsDefault {
			continue
		}
		err := u.svc.DeleteManagedObjectStoragePolicyVersion(ctx, &request.DeleteManagedObjectStoragePolicyVersionRequest{
			ServiceUUID: u.serviceUUID, Name: name, VersionID: v.VersionID,
		})
		if err != nil && !isNotFound(err) {
			return fmt.Errorf("delete version %s of policy %s: %w", v.VersionID, name, err)
		}
		left--
	}
	_, err = u.svc.CreateManagedObjectStoragePolicyVersion(ctx, &request.CreateManagedObjectStoragePolicyVersionRequest{
		ServiceUUID: u.serviceUUID, Name: name, Document: document, IsDefault: true,
	})
	if err != nil {
		return fmt.Errorf("update policy %s: %w", name, err)
	}
	return nil
}

func (u *UpCloud) DeletePolicy(ctx context.Context, name string) error {
	err := u.svc.DeleteManagedObjectStoragePolicy(ctx, &request.DeleteManagedObjectStoragePolicyRequest{
		ServiceUUID: u.serviceUUID, Name: name,
	})
	if err != nil && !isNotFound(err) {
		return fmt.Errorf("delete policy %s: %w", name, err)
	}
	return nil
}

func (u *UpCloud) EnsureUserPolicy(ctx context.Context, username, policy string) error {
	attached, err := u.svc.GetManagedObjectStorageUserPolicies(ctx, &request.GetManagedObjectStorageUserPoliciesRequest{
		ServiceUUID: u.serviceUUID, Username: username,
	})
	if err != nil {
		return fmt.Errorf("list policies of %s: %w", username, err)
	}
	for _, p := range attached {
		if p.Name == policy {
			return nil
		}
	}
	err = u.svc.AttachManagedObjectStorageUserPolicy(ctx, &request.AttachManagedObjectStorageUserPolicyRequest{
		ServiceUUID: u.serviceUUID, Username: username, Name: policy,
	})
	if err != nil && !isConflict(err) {
		return fmt.Errorf("attach policy %s to %s: %w", policy, username, err)
	}
	return nil
}

func (u *UpCloud) AccessKeys(ctx context.Context, username string) ([]string, error) {
	keys, err := u.svc.GetManagedObjectStorageUserAccessKeys(ctx, &request.GetManagedObjectStorageUserAccessKeysRequest{
		ServiceUUID: u.serviceUUID, Username: username,
	})
	if err != nil {
		return nil, fmt.Errorf("list access keys of %s: %w", username, err)
	}
	ids := make([]string, 0, len(keys))
	for _, k := range keys {
		ids = append(ids, k.AccessKeyID)
	}
	return ids, nil
}

func (u *UpCloud) CreateAccessKey(ctx context.Context, username string) (string, string, error) {
	k, err := u.svc.CreateManagedObjectStorageUserAccessKey(ctx, &request.CreateManagedObjectStorageUserAccessKeyRequest{
		ServiceUUID: u.serviceUUID, Username: username,
	})
	if err != nil {
		return "", "", fmt.Errorf("create access key for %s: %w", username, err)
	}
	if k.SecretAccessKey == nil {
		return "", "", fmt.Errorf("create access key for %s: no secret in the response", username)
	}
	return k.AccessKeyID, *k.SecretAccessKey, nil
}

func (u *UpCloud) DeleteAccessKey(ctx context.Context, username, id string) error {
	err := u.svc.DeleteManagedObjectStorageUserAccessKey(ctx, &request.DeleteManagedObjectStorageUserAccessKeyRequest{
		ServiceUUID: u.serviceUUID, Username: username, AccessKeyID: id,
	})
	if err != nil && !isNotFound(err) {
		return fmt.Errorf("delete access key %s of %s: %w", id, username, err)
	}
	return nil
}
