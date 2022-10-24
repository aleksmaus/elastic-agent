// Copyright Elasticsearch B.V. and/or licensed to Elasticsearch B.V. under one
// or more contributor license agreements. Licensed under the Elastic License;
// you may not use this file except in compliance with the Elastic License.

package kubernetes

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/elastic/elastic-agent-autodiscover/kubernetes"
	"github.com/elastic/elastic-agent-autodiscover/kubernetes/metadata"
	"github.com/elastic/elastic-agent-libs/mapstr"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

func TestGenerateServiceData(t *testing.T) {
	service := &kubernetes.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "testsvc",
			UID:       types.UID(uid),
			Namespace: "testns",
			Labels: map[string]string{
				"foo":        "bar",
				"with-dash":  "dash-value",
				"with/slash": "some/path",
			},
			Annotations: map[string]string{
				"baz": "ban",
			},
		},
		TypeMeta: metav1.TypeMeta{
			Kind:       "Service",
			APIVersion: "v1",
		},
		Spec: v1.ServiceSpec{
			ClusterIP: "1.2.3.4",
			Selector: map[string]string{
				"app":   "istiod",
				"istio": "pilot",
			},
		},
	}

	data := generateServiceData(
		service,
		&svcMeta{},
		mapstr.M{
			"nsa": "nsb",
		})

	mapping := map[string]interface{}{
		"service": mapstr.M{
			"uid":  string(service.GetUID()),
			"name": service.GetName(),
			"ip":   service.Spec.ClusterIP,
		},
		"namespace_annotations": mapstr.M{
			"nsa": "nsb",
		},
		"annotations": mapstr.M{
			"baz": "ban",
		},
		"labels": mapstr.M{
			"foo":        "bar",
			"with-dash":  "dash-value",
			"with/slash": "some/path",
		},
	}

	processors := map[string]interface{}{
		"orchestrator": mapstr.M{
			"cluster": mapstr.M{
				"name": "devcluster",
				"url":  "8.8.8.8:9090"},
		}, "kubernetes": mapstr.M{
			"service": mapstr.M{
				"uid":  string(service.GetUID()),
				"name": service.GetName(),
				"ip":   "1.2.3.4",
			},
			"labels": mapstr.M{
				"foo":        "bar",
				"with-dash":  "dash-value",
				"with/slash": "some/path",
			},
			"annotations": mapstr.M{
				"baz": "ban",
			},
		},
	}

	assert.Equal(t, service, data.service)
	assert.Equal(t, mapping, data.mapping)
	for _, v := range data.processors {
		k, _ := v["add_fields"].(map[string]interface{})
		target, _ := k["target"].(string)
		fields := k["fields"]
		assert.Equal(t, processors[target], fields)
	}
}

type svcMeta struct{}

// Generate generates svc metadata from a resource object
// All Kubernetes fields that need to be stored under kubernetes. prefix are populated by
// GenerateK8s method while fields that are part of ECS are generated by GenerateECS method
func (s *svcMeta) Generate(obj kubernetes.Resource, opts ...metadata.FieldOptions) mapstr.M {
	ecsFields := s.GenerateECS(obj)
	meta := mapstr.M{
		"kubernetes": s.GenerateK8s(obj, opts...),
	}
	meta.DeepUpdate(ecsFields)
	return meta
}

// GenerateECS generates svc ECS metadata from a resource object
func (s *svcMeta) GenerateECS(obj kubernetes.Resource) mapstr.M {
	return mapstr.M{
		"orchestrator": mapstr.M{
			"cluster": mapstr.M{
				"name": "devcluster",
				"url":  "8.8.8.8:9090",
			},
		},
	}
}

// GenerateK8s generates svc metadata from a resource object
func (s *svcMeta) GenerateK8s(obj kubernetes.Resource, opts ...metadata.FieldOptions) mapstr.M {
	k8sNode, _ := obj.(*kubernetes.Service)
	return mapstr.M{
		"service": mapstr.M{
			"uid":  string(k8sNode.GetUID()),
			"name": k8sNode.GetName(),
			"ip":   "1.2.3.4",
		},
		"labels": mapstr.M{
			"foo":        "bar",
			"with-dash":  "dash-value",
			"with/slash": "some/path",
		},
		"annotations": mapstr.M{
			"baz": "ban",
		},
	}
}

// GenerateFromName generates svc metadata from a node name
func (s *svcMeta) GenerateFromName(name string, opts ...metadata.FieldOptions) mapstr.M {
	return nil
}
