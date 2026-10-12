package pipeline

import (
	"context"
	"time"

	"github.com/twmb/franz-go/pkg/kgo"
)

type kafkaLog struct{ client *kgo.Client }

// NewKafka returns a Log that writes to the given brokers. It connects
// lazily: a broker that is down at start does not stop the pipeline, and
// records fail until it is back.
func NewKafka(brokers []string) (Log, error) {
	client, err := kgo.NewClient(
		kgo.SeedBrokers(brokers...),
		kgo.ProducerLinger(50*time.Millisecond),
		kgo.RecordDeliveryTimeout(20*time.Second),
		kgo.MaxBufferedRecords(50_000),
		kgo.AllowAutoTopicCreation(),
	)
	if err != nil {
		return nil, err
	}
	return &kafkaLog{client: client}, nil
}

// Send hands the record to the client without waiting; when the client's
// own buffer is full the record fails immediately.
func (k *kafkaLog) Send(m Message, done func(error)) {
	k.client.TryProduce(context.Background(), &kgo.Record{Topic: m.Topic, Key: m.Key, Value: m.Value}, func(_ *kgo.Record, err error) {
		done(err)
	})
}

func (k *kafkaLog) Close(ctx context.Context) error {
	err := k.client.Flush(ctx)
	k.client.Close()
	return err
}
