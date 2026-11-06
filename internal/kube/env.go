package kube

import (
	"context"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ConfigMapData returns a ConfigMap's values, empty if there's no such
// ConfigMap.
func (c *Client) ConfigMapData(ctx context.Context, ns, name string) (map[string]string, error) {
	cm, err := c.Core.CoreV1().ConfigMaps(ns).Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return map[string]string{}, nil
	}
	if err != nil {
		return nil, err
	}
	out := make(map[string]string, len(cm.Data))
	for k, v := range cm.Data {
		out[k] = v
	}
	return out, nil
}

// ApplyConfigMap makes ConfigMap name hold exactly data, labelled as
// mcloud's (the maxcloud controller backs up ConfigMaps so marked). Like
// ApplySecret, it's updated in place, never deleted, and an update made
// since it was read fails instead of being overwritten.
func (c *Client) ApplyConfigMap(ctx context.Context, ns, name string, data map[string]string) error {
	cms := c.Core.CoreV1().ConfigMaps(ns)
	cm, err := cms.Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		cm = &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Name: name, Labels: map[string]string{}, Annotations: map[string]string{}},
			Data:       data,
		}
		markAppObject(&cm.ObjectMeta)
		_, err = cms.Create(ctx, cm, metav1.CreateOptions{FieldManager: FieldManager})
		return err
	}
	if err != nil {
		return err
	}
	markAppObject(&cm.ObjectMeta)
	cm.Data = data
	_, err = cms.Update(ctx, cm, metav1.UpdateOptions{FieldManager: FieldManager})
	return err
}

// markAppObject puts mcloud's label and annotation on an object it keeps
// for an app (app-secrets, app-env).
func markAppObject(m *metav1.ObjectMeta) {
	if m.Labels == nil {
		m.Labels = map[string]string{}
	}
	if m.Annotations == nil {
		m.Annotations = map[string]string{}
	}
	for k, v := range appSecretsLabels {
		m.Labels[k] = v
	}
	for k, v := range appSecretsAnnotations {
		m.Annotations[k] = v
	}
}
