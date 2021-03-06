/*
Copyright AppsCode Inc. and Contributors

Licensed under the AppsCode Community License 1.0.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    https://github.com/appscode/licenses/raw/1.0.0/AppsCode-Community-1.0.0.md

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controller

import (
	"context"

	api "kubedb.dev/apimachinery/apis/kubedb/v1alpha2"
	"kubedb.dev/apimachinery/pkg/eventer"

	"github.com/appscode/go/log"
	"github.com/appscode/go/types"
	core "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	kutil "kmodules.xyz/client-go"
	core_util "kmodules.xyz/client-go/core/v1"
	mona "kmodules.xyz/monitoring-agent-api/api/v1"
	ofst "kmodules.xyz/offshoot-api/api/v1"
)

func (c *Controller) ensureGoverningService(db *api.Postgres) error {
	meta := metav1.ObjectMeta{
		Name:      db.GoverningServiceName(),
		Namespace: db.Namespace,
	}

	owner := metav1.NewControllerRef(db, api.SchemeGroupVersion.WithKind(api.ResourceKindPostgres))

	_, vt, err := core_util.CreateOrPatchService(context.TODO(), c.Client, meta, func(in *core.Service) *core.Service {
		core_util.EnsureOwnerReference(&in.ObjectMeta, owner)
		in.Labels = db.OffshootLabels()

		in.Spec.Type = core.ServiceTypeClusterIP
		in.Spec.ClusterIP = core.ClusterIPNone
		in.Spec.Selector = db.OffshootSelectors()
		in.Spec.PublishNotReadyAddresses = true

		return in
	}, metav1.PatchOptions{})
	if err == nil && (vt == kutil.VerbCreated || vt == kutil.VerbPatched) {
		c.Recorder.Eventf(
			db,
			core.EventTypeNormal,
			eventer.EventReasonSuccessful,
			"Successfully %s governing service",
			vt,
		)
	}

	return err
}

func (c *Controller) ensureService(db *api.Postgres) (kutil.VerbType, error) {
	// create database Service
	vt1, err := c.ensurePrimaryService(db)
	if err != nil {
		return kutil.VerbUnchanged, err
	} else if vt1 != kutil.VerbUnchanged {
		c.Recorder.Eventf(
			db,
			core.EventTypeNormal,
			eventer.EventReasonSuccessful,
			"Successfully %s Service",
			vt1,
		)
	}

	// create standby database Service
	vt2 := kutil.VerbUnchanged
	replicas := int32(1)
	if db.Spec.Replicas != nil {
		replicas = types.Int32(db.Spec.Replicas)
	}
	if replicas > 1 {
		vt2, err = c.ensureStandbyService(db)
		if err != nil {
			return kutil.VerbUnchanged, err
		} else if vt2 != kutil.VerbUnchanged {
			c.Recorder.Eventf(
				db,
				core.EventTypeNormal,
				eventer.EventReasonSuccessful,
				"Successfully %s Service",
				vt2,
			)
		}
	}

	if vt1 == kutil.VerbCreated && vt2 == kutil.VerbCreated {
		return kutil.VerbCreated, nil
	} else if vt1 == kutil.VerbPatched || vt2 == kutil.VerbPatched {
		return kutil.VerbPatched, nil
	}

	return kutil.VerbUnchanged, nil
}

func (c *Controller) ensurePrimaryService(db *api.Postgres) (kutil.VerbType, error) {
	meta := metav1.ObjectMeta{
		Name:      db.OffshootName(),
		Namespace: db.Namespace,
	}

	owner := metav1.NewControllerRef(db, api.SchemeGroupVersion.WithKind(api.ResourceKindPostgres))

	_, ok, err := core_util.CreateOrPatchService(context.TODO(), c.Client, meta, func(in *core.Service) *core.Service {
		core_util.EnsureOwnerReference(&in.ObjectMeta, owner)
		in.Labels = db.OffshootLabels()
		in.Annotations = db.Spec.ServiceTemplate.Annotations

		in.Spec.Selector = db.OffshootSelectors()
		in.Spec.Selector[api.PostgresLabelRole] = api.PostgresPodPrimary
		in.Spec.Ports = ofst.MergeServicePorts(
			core_util.MergeServicePorts(in.Spec.Ports, []core.ServicePort{
				{
					Name:       api.PostgresPrimaryServicePortName,
					Port:       api.PostgresDatabasePort,
					TargetPort: intstr.FromString(api.PostgresDatabasePortName),
				},
			}),
			db.Spec.ServiceTemplate.Spec.Ports,
		)

		if db.Spec.ServiceTemplate.Spec.ClusterIP != "" {
			in.Spec.ClusterIP = db.Spec.ServiceTemplate.Spec.ClusterIP
		}
		if db.Spec.ServiceTemplate.Spec.Type != "" {
			in.Spec.Type = db.Spec.ServiceTemplate.Spec.Type
		}
		in.Spec.ExternalIPs = db.Spec.ServiceTemplate.Spec.ExternalIPs
		in.Spec.LoadBalancerIP = db.Spec.ServiceTemplate.Spec.LoadBalancerIP
		in.Spec.LoadBalancerSourceRanges = db.Spec.ServiceTemplate.Spec.LoadBalancerSourceRanges
		in.Spec.ExternalTrafficPolicy = db.Spec.ServiceTemplate.Spec.ExternalTrafficPolicy
		if db.Spec.ServiceTemplate.Spec.HealthCheckNodePort > 0 {
			in.Spec.HealthCheckNodePort = db.Spec.ServiceTemplate.Spec.HealthCheckNodePort
		}
		return in
	}, metav1.PatchOptions{})
	return ok, err
}

func (c *Controller) ensureStandbyService(db *api.Postgres) (kutil.VerbType, error) {
	meta := metav1.ObjectMeta{
		Name:      db.StandbyServiceName(),
		Namespace: db.Namespace,
	}

	owner := metav1.NewControllerRef(db, api.SchemeGroupVersion.WithKind(api.ResourceKindPostgres))

	_, ok, err := core_util.CreateOrPatchService(context.TODO(), c.Client, meta, func(in *core.Service) *core.Service {
		core_util.EnsureOwnerReference(&in.ObjectMeta, owner)
		in.Labels = db.OffshootLabels()
		in.Annotations = db.Spec.ReplicaServiceTemplate.Annotations

		in.Spec.Selector = db.OffshootSelectors()
		in.Spec.Selector[api.PostgresLabelRole] = api.PostgresPodStandby
		in.Spec.Ports = ofst.MergeServicePorts(
			core_util.MergeServicePorts(in.Spec.Ports, []core.ServicePort{
				{
					Name:       api.PostgresStandbyServicePortName,
					Port:       api.PostgresDatabasePort,
					TargetPort: intstr.FromString(api.PostgresDatabasePortName),
				},
			}),
			db.Spec.ReplicaServiceTemplate.Spec.Ports,
		)

		if db.Spec.ReplicaServiceTemplate.Spec.ClusterIP != "" {
			in.Spec.ClusterIP = db.Spec.ReplicaServiceTemplate.Spec.ClusterIP
		}
		if db.Spec.ReplicaServiceTemplate.Spec.Type != "" {
			in.Spec.Type = db.Spec.ReplicaServiceTemplate.Spec.Type
		}
		in.Spec.ExternalIPs = db.Spec.ReplicaServiceTemplate.Spec.ExternalIPs
		in.Spec.LoadBalancerIP = db.Spec.ReplicaServiceTemplate.Spec.LoadBalancerIP
		in.Spec.LoadBalancerSourceRanges = db.Spec.ReplicaServiceTemplate.Spec.LoadBalancerSourceRanges
		in.Spec.ExternalTrafficPolicy = db.Spec.ReplicaServiceTemplate.Spec.ExternalTrafficPolicy
		if db.Spec.ReplicaServiceTemplate.Spec.HealthCheckNodePort > 0 {
			in.Spec.HealthCheckNodePort = db.Spec.ReplicaServiceTemplate.Spec.HealthCheckNodePort
		}
		return in
	}, metav1.PatchOptions{})
	return ok, err
}

func (c *Controller) ensureStatsService(db *api.Postgres) (kutil.VerbType, error) {
	// return if monitoring is not prometheus
	if db.Spec.Monitor == nil || db.Spec.Monitor.Agent.Vendor() != mona.VendorPrometheus {
		log.Infoln("postgres.spec.monitor.agent is not provided by prometheus.io")
		return kutil.VerbUnchanged, nil
	}

	owner := metav1.NewControllerRef(db, api.SchemeGroupVersion.WithKind(api.ResourceKindPostgres))

	// reconcile stats service
	meta := metav1.ObjectMeta{
		Name:      db.StatsService().ServiceName(),
		Namespace: db.Namespace,
	}
	_, vt, err := core_util.CreateOrPatchService(context.TODO(), c.Client, meta, func(in *core.Service) *core.Service {
		core_util.EnsureOwnerReference(&in.ObjectMeta, owner)
		in.Labels = db.StatsServiceLabels()
		in.Spec.Selector = db.OffshootSelectors()
		in.Spec.Ports = core_util.MergeServicePorts(in.Spec.Ports, []core.ServicePort{
			{
				Name:       mona.PrometheusExporterPortName,
				Protocol:   core.ProtocolTCP,
				Port:       db.Spec.Monitor.Prometheus.Exporter.Port,
				TargetPort: intstr.FromString(mona.PrometheusExporterPortName),
			},
		})
		return in
	}, metav1.PatchOptions{})
	if err != nil {
		return kutil.VerbUnchanged, err
	} else if vt != kutil.VerbUnchanged {
		c.Recorder.Eventf(
			db,
			core.EventTypeNormal,
			eventer.EventReasonSuccessful,
			"Successfully %s stats service",
			vt,
		)
	}
	return vt, nil
}
