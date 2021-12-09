package policies_test

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"
	"github.com/godbus/dbus/v5/introspect"
	"github.com/godbus/dbus/v5/prop"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/termie/go-shutil"
	"github.com/ubuntu/adsys/internal/consts"
	"github.com/ubuntu/adsys/internal/policies"
	"github.com/ubuntu/adsys/internal/policies/entry"
	"github.com/ubuntu/adsys/internal/testutils"
)

var update bool

func TestDumpPolicies(t *testing.T) {
	t.Parallel()

	bus := testutils.NewDbusConn(t)

	hostname, err := os.Hostname()
	require.NoError(t, err, "Setup: failed to get hostname")

	tests := map[string]struct {
		cacheUser      string
		cacheMachine   string
		target         string
		withRules      bool
		withOverridden bool

		wantErr bool
	}{
		"One GPO User": {
			cacheUser: "one_gpo",
		},
		"One GPO Machine": {
			cacheMachine: "one_gpo",
			target:       hostname,
		},
		"One GPO User + Machine": {
			cacheUser:    "one_gpo",
			cacheMachine: "one_gpo_other",
		},
		"Multiple GPOs": {
			cacheUser: "two_gpos_no_override",
		},

		// Show rules
		"One GPO with rules": {
			cacheUser: "one_gpo",
			withRules: true,
		},
		"Machine only GPO with rules": {
			cacheMachine: "one_gpo",
			target:       hostname,
			withRules:    true,
		},
		"Multiple GPOs with rules, no override": {
			cacheUser: "two_gpos_no_override",
			withRules: true,
		},
		"Multiple GPOs with rules, override hidden": {
			cacheUser: "two_gpos_with_overrides",
			withRules: true,
		},
		"Multiple GPOs with rules, override, shown": {
			cacheUser:      "two_gpos_with_overrides",
			withRules:      true,
			withOverridden: true,
		},

		// machine and user GPO with overrides between machine and user
		"Overrides between machine and user GPOs, hidden": {
			cacheUser:    "one_gpo",
			cacheMachine: "two_gpos_override_one_gpo",
			withRules:    true,
		},
		"Overrides between machine and user GPOs, shown": {
			cacheUser:      "one_gpo",
			cacheMachine:   "two_gpos_override_one_gpo",
			withRules:      true,
			withOverridden: true,
		},

		// Edge cases
		"Same GPO Machine and User": {
			cacheUser:    "one_gpo",
			cacheMachine: "one_gpo",
		},
		"Same GPO Machine and User with rules": {
			cacheUser:    "one_gpo",
			cacheMachine: "one_gpo",
			withRules:    true,
		},
		"Same GPO Machine and User with rules and overrides": {
			cacheUser:      "one_gpo",
			cacheMachine:   "one_gpo",
			withRules:      true,
			withOverridden: true,
		},

		// Error cases
		"Error on missing target cache": {
			wantErr: true,
		},
		"Error on missing machine cache when targeting user": {
			cacheUser:    "one_gpo",
			cacheMachine: "-",
			wantErr:      true,
		},
	}

	for name, tc := range tests {
		tc := tc
		name := name
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			cacheDir := t.TempDir()
			m, err := policies.New(bus, policies.WithCacheDir(cacheDir))
			require.NoError(t, err, "Setup: couldn’t get a new policy manager")

			err = os.MkdirAll(filepath.Join(cacheDir, entry.GPORulesCacheBaseName), 0750)
			require.NoError(t, err, "Setup: cant not create gpo rule cache directory")

			if tc.cacheUser != "" {
				err := shutil.CopyFile(filepath.Join("testdata", "cache", tc.cacheUser), filepath.Join(cacheDir, entry.GPORulesCacheBaseName, "user"), false)
				require.NoError(t, err, "Setup: couldn’t copy user cache")
			}
			if tc.cacheMachine == "" {
				f, err := os.Create(filepath.Join(cacheDir, entry.GPORulesCacheBaseName, hostname))
				require.NoError(t, err, "Setup: failed to create empty machine cache file")
				f.Close()
			} else if tc.cacheMachine != "-" {
				err := shutil.CopyFile(filepath.Join("testdata", "cache", tc.cacheMachine), filepath.Join(cacheDir, entry.GPORulesCacheBaseName, hostname), false)
				require.NoError(t, err, "Setup: couldn’t copy machine cache")
			}

			if tc.target == "" {
				tc.target = "user"
			}
			got, err := m.DumpPolicies(context.Background(), tc.target, tc.withRules, tc.withOverridden)
			if tc.wantErr {
				require.Error(t, err, "DumpPolicies should return an error but got none")
				return
			}
			require.NoError(t, err, "DumpPolicies should return no error but got one")

			goldPath := filepath.Join("testdata", "golden", name)
			// Update golden file
			if update {
				t.Logf("updating golden file %s", goldPath)
				err = os.WriteFile(goldPath, []byte(got), 0600)
				require.NoError(t, err, "Cannot write golden file")
			}
			want, err := os.ReadFile(goldPath)
			require.NoError(t, err, "Cannot load policy golden file")

			require.Equal(t, string(want), got, "DumpPolicies returned expected output")
		})
	}
}

func TestApplyPolicy(t *testing.T) {
	//t.Parallel()

	bus := testutils.NewDbusConn(t)

	subscriptionDbus := bus.Object(consts.SubcriptionDbusRegisteredName,
		dbus.ObjectPath(consts.SubcriptionDbusObjectPath))

	tests := map[string]struct {
		gposFile                     string
		secondCallWithNoRules        bool
		makeDirReadOnly              string
		isNotSubscribed              bool
		secondCallWithNoSubscription bool

		wantErr bool
	}{
		"succeed": {gposFile: "all_entry_types.gpos"},
		"second call with no rules deletes everything": {gposFile: "all_entry_types.gpos", secondCallWithNoRules: true},

		// no subscription filterings
		"no subscription is only dconf content":                                       {gposFile: "all_entry_types.gpos", isNotSubscribed: true},
		"second call with no subscription should remove everything but dconf content": {gposFile: "all_entry_types.gpos", secondCallWithNoSubscription: true},

		"dconf apply policy fails":     {gposFile: "dconf_failing.gpos", wantErr: true},
		"privilege apply policy fails": {makeDirReadOnly: "etc/sudoers.d", gposFile: "all_entry_types.gpos", wantErr: true},
	}
	for name, tc := range tests {
		tc := tc
		name := name
		t.Run(name, func(t *testing.T) {
			// We change the dbus returned values to simulate a subscription
			//t.Parallel()

			gpos, err := entry.NewGPOs(filepath.Join("testdata", tc.gposFile))
			require.NoError(t, err, "Setup: can not load gpo list")

			fakeRootDir := t.TempDir()
			cacheDir := filepath.Join(fakeRootDir, "var", "cache", "adsys")
			dconfDir := filepath.Join(fakeRootDir, "etc", "dconf")
			policyKitDir := filepath.Join(fakeRootDir, "etc", "polkit-1")
			sudoersDir := filepath.Join(fakeRootDir, "etc", "sudoers.d")

			status := "enabled"
			if tc.isNotSubscribed {
				status = "disabled"
			}
			require.NoError(t, subscriptionDbus.SetProperty(consts.SubcriptionDbusInterface+".Status", status), "Setup: can not set subscription status to %q", status)
			defer func() {
				require.NoError(t, subscriptionDbus.SetProperty(consts.SubcriptionDbusInterface+".Status", ""), "Teardown: can not restore subscription status")
			}()

			m, err := policies.New(bus,
				policies.WithCacheDir(cacheDir),
				policies.WithDconfDir(dconfDir),
				policies.WithPolicyKitDir(policyKitDir),
				policies.WithSudoersDir(sudoersDir),
			)
			require.NoError(t, err, "Setup: couldn’t get a new policy manager")

			err = os.MkdirAll(filepath.Join(cacheDir, entry.GPORulesCacheBaseName), 0750)
			require.NoError(t, err, "Setup: cant not create gpo rule cache directory")

			if tc.makeDirReadOnly != "" {
				require.NoError(t, os.MkdirAll(filepath.Join(fakeRootDir, tc.makeDirReadOnly), 0750), "Setup: can not create directory")
				testutils.MakeReadOnly(t, filepath.Join(fakeRootDir, tc.makeDirReadOnly))
			}

			err = m.ApplyPolicy(context.Background(), "hostname", true, gpos)
			if tc.wantErr {
				require.Error(t, err, "ApplyPolicy should return an error but got none")
				return
			}
			require.NoError(t, err, "ApplyPolicy should return no error but got one")

			var runSecondCall bool
			if tc.secondCallWithNoRules {
				runSecondCall = true
				gpos = nil
			} else if tc.secondCallWithNoSubscription {
				runSecondCall = true
				require.NoError(t, subscriptionDbus.SetProperty(consts.SubcriptionDbusInterface+".Status", "disabled"), "Setup: can not set subscription status for second call to disabled")
			}
			if runSecondCall {
				err = m.ApplyPolicy(context.Background(), "hostname", true, gpos)
				require.NoError(t, err, "ApplyPolicy should return no error but got one")
			}

			testutils.CompareTreesWithFiltering(t, fakeRootDir, filepath.Join("testdata", "golden", name), update)
		})
	}
}

func TestLastUpdateFor(t *testing.T) {
	t.Parallel()

	bus := testutils.NewDbusConn(t)

	hostname, err := os.Hostname()
	require.NoError(t, err, "Setup: failed to get hostname")

	tests := map[string]struct {
		target    string
		isMachine bool

		wantErr bool
	}{
		"Returns user's last update time":       {target: "user"},
		"Returns machine's last update time":    {target: hostname, isMachine: true},
		"Target is ignored for machine request": {target: "does_not_exit", isMachine: true},

		// Error cases
		"Target does not exist": {target: "does_not_exit", wantErr: true},
	}

	for name, tc := range tests {
		tc := tc
		name := name
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			cacheDir := t.TempDir()
			m, err := policies.New(bus, policies.WithCacheDir(cacheDir))
			require.NoError(t, err, "Setup: couldn’t get a new policy manager")

			err = os.MkdirAll(filepath.Join(cacheDir, entry.GPORulesCacheBaseName), 0750)
			require.NoError(t, err, "Setup: cant not create gpo rule cache directory")

			start := time.Now()
			// Starts and ends are monotic, while os.Stat is wall clock, we have to wait for measuring difference…
			time.Sleep(100 * time.Millisecond)
			f, err := os.Create(filepath.Join(cacheDir, entry.GPORulesCacheBaseName, "user"))
			require.NoError(t, err, "Setup: couldn’t copy user cache")
			f.Close()
			f, err = os.Create(filepath.Join(cacheDir, entry.GPORulesCacheBaseName, hostname))
			require.NoError(t, err, "Setup: couldn’t copy user cache")
			f.Close()

			got, err := m.LastUpdateFor(context.Background(), tc.target, tc.isMachine)
			if tc.wantErr {
				require.Error(t, err, "LastUpdateFor should return an error but got none")
				return
			}
			require.NoError(t, err, "LastUpdateFor should return no error but got one")
			end := time.Now()

			assert.True(t, got.After(start), "expected got after start")
			assert.True(t, got.Before(end), "expected got before end")
		})
	}
}

func TestGetStatus(t *testing.T) {
	//t.Parallel()

	bus := testutils.NewDbusConn(t)
	subscriptionDbus := bus.Object(consts.SubcriptionDbusRegisteredName,
		dbus.ObjectPath(consts.SubcriptionDbusObjectPath))

	tests := map[string]struct {
		status string

		want bool
	}{
		"returns enablement status (enabled)":  {status: "enabled", want: true},
		"returns enablement status (disabled)": {status: "somethingelse", want: false},
	}

	for name, tc := range tests {
		tc := tc
		name := name
		t.Run(name, func(t *testing.T) {
			// We change the dbus returned values to simulate a subscription
			//t.Parallel()

			require.NoError(t, subscriptionDbus.SetProperty(consts.SubcriptionDbusInterface+".Status", tc.status), "Setup: can not set subscription status to %q", tc.status)
			defer func() {
				require.NoError(t, subscriptionDbus.SetProperty(consts.SubcriptionDbusInterface+".Status", ""), "Teardown: can not restore subscription status")
			}()

			cacheDir := t.TempDir()
			m, err := policies.New(bus, policies.WithCacheDir(cacheDir))
			require.NoError(t, err, "Setup: couldn’t get a new policy manager")

			// force a refresh
			_ = m.GetSubcriptionState(context.Background())

			got := m.GetStatus()
			assert.Equal(t, tc.want, got, "GetStatus should return %q but got %q", tc.want, got)
		})
	}
}

func TestMain(m *testing.M) {
	flag.BoolVar(&update, "update", false, "update golden files")
	flag.Parse()

	// Don’t setup samba or sssd for mock helpers
	if !strings.Contains(strings.Join(os.Args, " "), "TestMock") {

		// Ubuntu Advantage
		defer testutils.StartLocalSystemBus()()

		conn, err := dbus.SystemBusPrivate()
		if err != nil {
			log.Fatalf("Setup: can’t get a private system bus: %v", err)
		}
		defer func() {
			if err = conn.Close(); err != nil {
				log.Fatalf("Teardown: can’t close system dbus connection: %v", err)
			}
		}()
		if err = conn.Auth(nil); err != nil {
			log.Fatalf("Setup: can’t auth on private system bus: %v", err)
		}
		if err = conn.Hello(); err != nil {
			log.Fatalf("Setup: can’t send hello message on private system bus: %v", err)
		}

		intro := fmt.Sprintf(`
		<node>
			<interface name="%s">
				<property name='Status' type='s' access="readwrite"/>
			</interface>%s%s</node>`, consts.SubcriptionDbusInterface, introspect.IntrospectDataString, prop.IntrospectDataString)
		ua := struct{}{}
		if err := conn.Export(ua, consts.SubcriptionDbusObjectPath, consts.SubcriptionDbusInterface); err != nil {
			log.Fatalf("Setup: could not export subscription object: %v", err)
		}

		propsSpec := map[string]map[string]*prop.Prop{
			consts.SubcriptionDbusInterface: {
				"Status": {
					Value:    "",
					Writable: true,
					Emit:     prop.EmitTrue,
					Callback: func(c *prop.Change) *dbus.Error { return nil },
				},
			},
		}
		_, err = prop.Export(conn, consts.SubcriptionDbusObjectPath, propsSpec)
		if err != nil {
			log.Fatalf("Setup: could not export property for subscription object: %v", err)
		}

		if err := conn.Export(introspect.Introspectable(intro), consts.SubcriptionDbusObjectPath,
			"org.freedesktop.DBus.Introspectable"); err != nil {
			log.Fatalf("Setup: could not export introspectable subscription object: %v", err)
		}

		reply, err := conn.RequestName(consts.SubcriptionDbusRegisteredName, dbus.NameFlagDoNotQueue)
		if err != nil {
			log.Fatalf("Setup: Failed to acquire sssd name on local system bus: %v", err)
		}
		if reply != dbus.RequestNameReplyPrimaryOwner {
			log.Fatalf("Setup: Failed to acquire sssd name on local system bus: name is already taken")
		}

	}

	m.Run()
}
