// +build ent

package command

import (
	"io/ioutil"
	"os"
	"strings"
	"testing"

	"github.com/hashicorp/nomad/api"
	"github.com/mitchellh/cli"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestQuotaApplyCommand_Implements(t *testing.T) {
	t.Parallel()
	var _ cli.Command = &QuotaApplyCommand{}
}

func TestQuotaApplyCommand_Fails(t *testing.T) {
	t.Parallel()
	ui := new(cli.MockUi)
	cmd := &QuotaApplyCommand{Meta: Meta{Ui: ui}}

	// Fails on misuse
	if code := cmd.Run([]string{"some", "bad", "args"}); code != 1 {
		t.Fatalf("expected exit code 1, got: %d", code)
	}
	if out := ui.ErrorWriter.String(); !strings.Contains(out, commandErrorText(cmd)) {
		t.Fatalf("expected help output, got: %s", out)
	}
	ui.ErrorWriter.Reset()

	if code := cmd.Run([]string{"-address=nope"}); code != 1 {
		t.Fatalf("expected exit code 1, got: %d", code)
	}
	if out := ui.ErrorWriter.String(); !strings.Contains(out, commandErrorText(cmd)) {
		t.Fatalf("name required error, got: %s", out)
	}
	ui.ErrorWriter.Reset()
}

func TestQuotaApplyCommand_Good_HCL(t *testing.T) {
	t.Parallel()

	// Create a server
	srv, client, url := testServer(t, true, nil)
	defer srv.Shutdown()

	ui := new(cli.MockUi)
	cmd := &QuotaApplyCommand{Meta: Meta{Ui: ui}}

	fh1, err := ioutil.TempFile("", "nomad")
	if err != nil {
		t.Fatalf("err: %s", err)
	}
	defer os.Remove(fh1.Name())
	if _, err := fh1.WriteString(defaultHclQuotaSpec); err != nil {
		t.Fatalf("err: %s", err)
	}

	// Create a quota spec
	if code := cmd.Run([]string{"-address=" + url, fh1.Name()}); code != 0 {
		t.Fatalf("expected exit 0, got: %d; %v", code, ui.ErrorWriter.String())
	}

	quotas, _, err := client.Quotas().List(nil)
	assert.Nil(t, err)
	assert.Len(t, quotas, 1)
}

func TestQuotaApplyCommand_Good_JSON(t *testing.T) {
	t.Parallel()

	// Create a server
	srv, client, url := testServer(t, true, nil)
	defer srv.Shutdown()

	ui := new(cli.MockUi)
	cmd := &QuotaApplyCommand{Meta: Meta{Ui: ui}}

	fh1, err := ioutil.TempFile("", "nomad")
	if err != nil {
		t.Fatalf("err: %s", err)
	}
	defer os.Remove(fh1.Name())
	if _, err := fh1.WriteString(defaultJsonQuotaSpec); err != nil {
		t.Fatalf("err: %s", err)
	}

	// Create a quota spec
	if code := cmd.Run([]string{"-address=" + url, "-json", fh1.Name()}); code != 0 {
		t.Fatalf("expected exit 0, got: %d; %v", code, ui.ErrorWriter.String())
	}

	quotas, _, err := client.Quotas().List(nil)
	assert.Nil(t, err)
	assert.Len(t, quotas, 1)
}

func TestQuotaApplyNetwork(t *testing.T) {
	t.Parallel()

	mbits := 20

	cases := []struct {
		hcl string
		q   *api.QuotaSpec
		err string
	}{{
		hcl: `limit {region = "global", region_limit {network {mbits = 20}}}`,
		q: &api.QuotaSpec{
			Limits: []*api.QuotaLimit{{
				Region: "global",
				RegionLimit: &api.Resources{
					Networks: []*api.NetworkResource{{
						MBits: &mbits,
					}},
				},
			}},
		},
		err: "",
	}, {
		hcl: `limit {region = "global", region_limit {network { mbits = 20, device = "eth0"}}}`,
		q:   nil,
		err: "1 error(s) occurred:\n\n* limit -> region_limit -> resources -> network -> invalid key: device",
	}}

	for _, c := range cases {
		t.Run(c.hcl, func(t *testing.T) {
			q, err := parseQuotaSpec([]byte(c.hcl))
			require.Equal(t, c.q, q)
			if c.err != "" {
				require.EqualError(t, err, c.err)
			}
		})
	}
}
