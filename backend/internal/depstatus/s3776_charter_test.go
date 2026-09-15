package depstatus

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestS3776Charter_Refresh(t *testing.T) {
	t.Parallel()

	t.Run("empty checker", func(t *testing.T) {
		t.Parallel()
		c := New()
		snap := c.refresh()
		require.Empty(t, snap.Resources)
		raw, err := json.Marshal(snap)
		require.NoError(t, err)
		assert.JSONEq(t, `{"resources":[]}`, string(raw))
	})

	t.Run("ok and unreachable without leaking the probe error", func(t *testing.T) {
		t.Parallel()
		c := New()
		c.Add(ObjectStore, func(context.Context) error { return nil })
		c.Add(MailBroker, func(context.Context) error {
			return errors.New(`Get "http://192.168.68.70:9000/foldex/": dial tcp 192.168.68.70:9000: i/o timeout`)
		})
		snap := c.refresh()
		require.Equal(t, []Resource{
			{ID: ObjectStore, State: StateOK},
			{ID: MailBroker, State: StateUnreachable},
		}, snap.Resources)
		raw, err := json.Marshal(snap)
		require.NoError(t, err)
		body := string(raw)
		for _, leak := range []string{"192.168.68.70", "9000", "timeout", "foldex/", "http://"} {
			assert.NotContains(t, body, leak)
		}
	})

	t.Run("AlwaysUnreachable does not dial", func(t *testing.T) {
		t.Parallel()
		c := New()
		c.Add(ObjectStore, AlwaysUnreachable)
		assert.Equal(t, StateUnreachable, c.refresh().Resources[0].State)
	})
}
