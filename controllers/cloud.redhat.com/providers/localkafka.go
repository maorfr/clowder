package providers

import (
	"fmt"

	strimzi "cloud.redhat.com/clowder/v2/apis/kafka.strimzi.io/v1beta1"

	//config "github.com/redhatinsights/app-common-go/pkg/api/v1" - to replace the import below at a future date
	"cloud.redhat.com/clowder/v2/controllers/cloud.redhat.com/config"
	"cloud.redhat.com/clowder/v2/controllers/cloud.redhat.com/utils"

	apps "k8s.io/api/apps/v1"
	core "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

type localKafka struct {
	Provider
	Config config.KafkaConfig
}

func (k *localKafka) Configure(config *config.AppConfig) {
	config.Kafka = &k.Config
}

func (k *localKafka) CreateTopic(nn types.NamespacedName, topic *strimzi.KafkaTopicSpec) error {
	topicName := fmt.Sprintf(
		"%s-%s-%s", topic.TopicName, k.Env.Name, k.Env.Namespace,
	)

	k.Config.Topics = append(
		k.Config.Topics,
		config.TopicConfig{
			Name:          topicName,
			RequestedName: topic.TopicName,
		},
	)

	return nil
}

func NewLocalKafka(p *Provider) (KafkaProvider, error) {

	port := 29092
	config := config.KafkaConfig{
		Topics: []config.TopicConfig{},
		Brokers: []config.BrokerConfig{{
			Hostname: fmt.Sprintf("%v-kafka.%v.svc", p.Env.Name, p.Env.Namespace),
			Port:     &port,
		}},
	}

	kafkaProvider := localKafka{
		Provider: *p,
		Config:   config,
	}

	err := makeLocalZookeeper(p)

	if err != nil {
		return &kafkaProvider, err
	}

	err = makeLocalKafka(p)

	if err != nil {
		return &kafkaProvider, err
	}

	return &kafkaProvider, nil
}

func makeLocalKafka(p *Provider) error {
	nn := types.NamespacedName{
		Name:      fmt.Sprintf("%v-kafka", p.Env.Name),
		Namespace: p.Env.Spec.Namespace,
	}

	dd := apps.Deployment{}

	update, err := utils.UpdateOrErr(p.Client.Get(p.Ctx, nn, &dd))

	if err != nil {
		return err
	}

	labels := p.Env.GetLabels()
	labels["env-app"] = nn.Name

	labeler := utils.MakeLabeler(nn, labels, p.Env)

	labeler(&dd)

	dd.Spec.Replicas = utils.Int32(1)
	dd.Spec.Selector = &metav1.LabelSelector{MatchLabels: labels}
	dd.Spec.Template.Spec.Volumes = []core.Volume{{
		Name: nn.Name,
		VolumeSource: core.VolumeSource{
			PersistentVolumeClaim: &core.PersistentVolumeClaimVolumeSource{
				ClaimName: nn.Name,
			},
		}},
		{
			Name: "mq-kafka-1",
			VolumeSource: core.VolumeSource{
				EmptyDir: &core.EmptyDirVolumeSource{},
			},
		},
		{
			Name: "mq-kafka-2",
			VolumeSource: core.VolumeSource{
				EmptyDir: &core.EmptyDirVolumeSource{},
			},
		},
	}
	dd.Spec.Template.ObjectMeta.Labels = labels

	envVars := []core.EnvVar{
		{
			Name: "KAFKA_ADVERTISED_LISTENERS", Value: "PLAINTEXT://" + nn.Name + ":29092, LOCAL://localhost:9092",
		},
		{
			Name:  "KAFKA_BROKER_ID",
			Value: "1",
		},
		{
			Name:  "KAFKA_OFFSETS_TOPIC_REPLICATION_FACTOR",
			Value: "1",
		},
		{
			Name:  "KAFKA_ZOOKEEPER_CONNECT",
			Value: p.Env.Name + "-zookeeper:32181",
		},
		{
			Name:  "LOG_DIR",
			Value: "/var/lib/mq-kafka",
		},
		{
			Name:  "KAFKA_LISTENER_SECURITY_PROTOCOL_MAP",
			Value: "PLAINTEXT:PLAINTEXT, LOCAL:PLAINTEXT",
		},
		{
			Name:  "KAFKA_INTER_BROKER_LISTENER_NAME",
			Value: "LOCAL",
		},
	}
	ports := []core.ContainerPort{
		{
			Name:          "kafka",
			ContainerPort: 9092,
		},
	}

	// TODO Readiness and Liveness probes

	c := core.Container{
		Name:  nn.Name,
		Image: "confluentinc/cp-kafka:latest",
		Env:   envVars,
		Ports: ports,
		VolumeMounts: []core.VolumeMount{
			{
				Name:      nn.Name,
				MountPath: "/var/lib/kafka",
			},
			{
				Name:      "mq-kafka-1",
				MountPath: "/etc/kafka/secrets",
			},
			{
				Name:      "mq-kafka-2",
				MountPath: "/var/lib/kafka/data",
			},
		},
	}

	dd.Spec.Template.Spec.Containers = []core.Container{c}
	dd.Spec.Template.SetLabels(labels)

	if _, err = update.Apply(p.Ctx, p.Client, &dd); err != nil {
		return err
	}

	s := core.Service{}
	update, err = utils.UpdateOrErr(p.Client.Get(p.Ctx, nn, &s))

	if err != nil {
		return err
	}

	labeler(&s)

	s.Spec.Selector = labels
	s.Spec.Ports = []core.ServicePort{{Name: "kafka", Port: 29092, Protocol: "TCP"}}

	if _, err = update.Apply(p.Ctx, p.Client, &s); err != nil {
		return err
	}

	pvc := core.PersistentVolumeClaim{}
	update, err = utils.UpdateOrErr(p.Client.Get(p.Ctx, nn, &pvc))

	if err != nil {
		return err
	}

	labeler(&pvc)

	pvc.Spec.AccessModes = []core.PersistentVolumeAccessMode{core.ReadWriteOnce}
	pvc.Spec.Resources = core.ResourceRequirements{
		Requests: core.ResourceList{
			core.ResourceName(core.ResourceStorage): resource.MustParse("1Gi"),
		},
	}

	if _, err = update.Apply(p.Ctx, p.Client, &pvc); err != nil {
		return err
	}

	return nil
}

func makeLocalZookeeper(p *Provider) error {

	nn := types.NamespacedName{
		Name:      fmt.Sprintf("%v-zookeeper", p.Env.Name),
		Namespace: p.Env.Spec.Namespace,
	}

	dd := apps.Deployment{}
	update, err := utils.UpdateOrErr(p.Client.Get(p.Ctx, nn, &dd))

	if err != nil {
		return err
	}

	labels := p.Env.GetLabels()
	labels["env-app"] = nn.Name

	labeler := utils.MakeLabeler(nn, labels, p.Env)

	labeler(&dd)

	dd.Spec.Replicas = utils.Int32(1)
	dd.Spec.Selector = &metav1.LabelSelector{MatchLabels: labels}
	dd.Spec.Template.Spec.Volumes = []core.Volume{{
		Name: nn.Name,
		VolumeSource: core.VolumeSource{
			PersistentVolumeClaim: &core.PersistentVolumeClaimVolumeSource{
				ClaimName: nn.Name,
			},
		}},
		{
			Name: "mq-zookeeper-1",
			VolumeSource: core.VolumeSource{
				EmptyDir: &core.EmptyDirVolumeSource{},
			},
		},
		{
			Name: "mq-zookeeper-2",
			VolumeSource: core.VolumeSource{
				EmptyDir: &core.EmptyDirVolumeSource{},
			},
		},
		{
			Name: "mq-zookeeper-3",
			VolumeSource: core.VolumeSource{
				EmptyDir: &core.EmptyDirVolumeSource{},
			},
		},
	}
	dd.Spec.Template.ObjectMeta.Labels = labels

	envVars := []core.EnvVar{
		{
			Name:  "ZOOKEEPER_INIT_LIMIT",
			Value: "10",
		},
		{
			Name:  "ZOOKEEPER_CLIENT_PORT",
			Value: "32181",
		},
		{
			Name:  "ZOOKEEPER_SERVER_ID",
			Value: "1",
		},
		{
			Name:  "ZOOKEEPER_SERVERS",
			Value: nn.Name + ":32181",
		},
		{
			Name:  "ZOOKEEPER_TICK_TIME",
			Value: "2000",
		},
		{
			Name:  "ZOOKEEPER_SYNC_LIMIT",
			Value: "10",
		},
	}
	ports := []core.ContainerPort{
		{
			Name:          "zookeeper",
			ContainerPort: 2181,
		},
		{
			Name:          "zookeeper-1",
			ContainerPort: 2888,
		},
		{
			Name:          "zookeeper-2",
			ContainerPort: 3888,
		},
	}

	// TODO Readiness and Liveness probes

	c := core.Container{
		Name:  nn.Name,
		Image: "confluentinc/cp-zookeeper:5.3.2",
		Env:   envVars,
		Ports: ports,
		VolumeMounts: []core.VolumeMount{
			{
				Name:      nn.Name,
				MountPath: "/var/lib/zookeeper",
			},
			{
				Name:      "mq-zookeeper-1",
				MountPath: "/etc/zookeeper/secrets",
			},
			{
				Name:      "mq-zookeeper-2",
				MountPath: "/var/lib/zookeeper/data",
			},
			{
				Name:      "mq-zookeeper-3",
				MountPath: "/var/lib/zookeeper/log",
			},
		},
	}

	dd.Spec.Template.Spec.Containers = []core.Container{c}
	dd.Spec.Template.SetLabels(labels)

	if _, err = update.Apply(p.Ctx, p.Client, &dd); err != nil {
		return err
	}

	s := core.Service{}
	update, err = utils.UpdateOrErr(p.Client.Get(p.Ctx, nn, &s))
	if err != nil {
		return err
	}

	servicePorts := []core.ServicePort{
		{
			Name: "zookeeper1", Port: 32181, Protocol: "TCP",
		},
		{
			Name: "zookeeper2", Port: 2888, Protocol: "TCP",
		},
		{
			Name: "zookeeper3", Port: 3888, Protocol: "TCP",
		},
	}

	labeler(&s)

	s.Spec.Selector = labels
	s.Spec.Ports = servicePorts

	if _, err = update.Apply(p.Ctx, p.Client, &s); err != nil {
		return err
	}

	pvc := core.PersistentVolumeClaim{}
	update, err = utils.UpdateOrErr(p.Client.Get(p.Ctx, nn, &pvc))
	if err != nil {
		return err
	}

	labeler(&pvc)

	pvc.Spec.AccessModes = []core.PersistentVolumeAccessMode{core.ReadWriteOnce}
	pvc.Spec.Resources = core.ResourceRequirements{
		Requests: core.ResourceList{
			core.ResourceName(core.ResourceStorage): resource.MustParse("1Gi"),
		},
	}

	if _, err = update.Apply(p.Ctx, p.Client, &pvc); err != nil {
		return err
	}
	return nil
}