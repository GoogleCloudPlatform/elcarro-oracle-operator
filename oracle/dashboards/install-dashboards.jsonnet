local k = import 'ksonnet/ksonnet.beta.3/k.libsonnet';

local pvc = k.core.v1.persistentVolumeClaim;

local kp = (import 'kube-prometheus/kube-prometheus.libsonnet') +
           (import 'kube-prometheus/kube-prometheus-all-namespaces.libsonnet') + {
  _config+:: {
    namespace: 'monitoring',
    prometheus+:: {
      namespaces: [],
    },
  },
  prometheus+:: {
    prometheus+: {
      spec+: {
        retention: '30d',
        storage: {
          volumeClaimTemplate:
            pvc.new() +
            pvc.mixin.spec.withAccessModes('ReadWriteOnce') +
            pvc.mixin.spec.resources.withRequests({ storage: '10Gi' }) +
            pvc.mixin.spec.withStorageClassName('csi-gce-pd'),
        },  // storage
      },  // spec
    },  // prometheus
  },  // prometheus
  grafanaDashboards+:: {  //  monitoring-mixin compatibility
    'db.json': (import 'db-dashboard.json'),
  },
  grafana+:: {
    dashboards+:: {  // use this method to import your dashboards to Grafana
      'db.json': (import 'db-dashboard.json'),
    },
  },
};


{ ['setup/0namespace-' + name]: kp.kubePrometheus[name] for name in std.objectFields(kp.kubePrometheus) } +
{
  ['setup/prometheus-operator-' + name]: kp.prometheusOperator[name]
  for name in std.filter((function(name) name != 'serviceMonitor'), std.objectFields(kp.prometheusOperator))
} +
// serviceMonitor is separated so that it can be created after the CRDs are ready
{ 'prometheus-operator-serviceMonitor': kp.prometheusOperator.serviceMonitor } +
{ ['prometheus-adapter-' + name]: kp.prometheusAdapter[name] for name in std.objectFields(kp.prometheusAdapter) } +
{ ['00namespace-' + name]: kp.kubePrometheus[name] for name in std.objectFields(kp.kubePrometheus) } +
{ ['0prometheus-operator-' + name]: kp.prometheusOperator[name] for name in std.objectFields(kp.prometheusOperator) } +
{ ['node-exporter-' + name]: kp.nodeExporter[name] for name in std.objectFields(kp.nodeExporter) } +
{ ['kube-state-metrics-' + name]: kp.kubeStateMetrics[name] for name in std.objectFields(kp.kubeStateMetrics) } +
{ ['alertmanager-' + name]: kp.alertmanager[name] for name in std.objectFields(kp.alertmanager) } +
{ ['prometheus-' + name]: kp.prometheus[name] for name in std.objectFields(kp.prometheus) } +
{ ['grafana-' + name]: kp.grafana[name] for name in std.objectFields(kp.grafana) }