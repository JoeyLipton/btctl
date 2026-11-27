// Package bluetooth - constants.go centralizes every tunable timeout, D-Bus
// name, formatting width, and executable name used across the package. It holds
// no logic beyond a few small accessors so the magic numbers live in one place.
package bluetooth

import (
	"time"

	"github.com/godbus/dbus/v5"
)

// D-Bus call timeouts and polling intervals used throughout the client.
const (
	dbusCallTimeout = 3 * time.Second

	// pairCallTimeout is the D-Bus call timeout for Pair() only; BlueZ blocks
	// inside Pair() while running SMP and calling agent methods. Slow devices
	// need generous headroom, but it stays below pairingTimeout.
	pairCallTimeout = 45 * time.Second

	// connectCallTimeout bounds Device1.Connect. BlueZ only replies once the
	// link and its profiles are up, which routinely takes several seconds for
	// audio devices (a classic page timeout alone is ~5 s), and BlueZ applies
	// its own connection timeouts, so this is a generous safety net.
	connectCallTimeout = 45 * time.Second

	connectedPollInterval = 100 * time.Millisecond

	// disconnectTimeout is how long to wait for Connected: false after
	// the BlueZ Disconnect() call returns. BLE devices take 2-8 seconds
	// to complete LE link termination cleanly.
	disconnectTimeout = 10 * time.Second

	// bondingTimeout is how long to wait for Bonded and Paired after Connected
	// for a device that was not bonded before Connect(). Some devices take 20+
	// seconds for the full SMP exchange in degraded RF conditions.
	bondingTimeout = 30 * time.Second

	// pairingTimeout bounds the whole connect-and-bond wait. Devices that need a
	// physical action (e.g. switching the MX Master 3 channel) need ample time.
	pairingTimeout = 120 * time.Second

	// agentResponseTimeout is how long agent methods wait for menu input; long
	// enough for the user to read the notification and act on the picker.
	agentResponseTimeout = 60 * time.Second

	// servicesResolvedTimeout waits for service discovery after Paired: true.
	servicesResolvedTimeout = 15 * time.Second

	// agentUnregisterTimeout is the context deadline for the deferred
	// UnregisterAgent call in runWithAgent.
	agentUnregisterTimeout = 3 * time.Second

	// statusClientTimeout is the context deadline used by runStatus when
	// creating its own short-lived Client. Status must complete fast for
	// polybar polling.
	statusClientTimeout = 5 * time.Second
)

// org.bluez.Device1 and adapter property names used by poll/watch helpers.
const (
	propConnected        = "Connected"
	propPaired           = "Paired"
	propBonded           = "Bonded"
	propTrusted          = "Trusted"
	propServicesResolved = "ServicesResolved"
	propPercentage       = "Percentage"

	// org.bluez.Adapter1 property names used by adapter operations.
	propAdapterPowered             = "Powered"
	propAdapterDiscoverable        = "Discoverable"
	propAdapterDiscoverableTimeout = "DiscoverableTimeout"
)

// Picker formatting widths for device and adapter list output.
const (
	deviceNameWidth         = 24
	adapterNameWidth        = 6
	adapterAddrWidth        = 17
	statusTagWidth          = 11
	statusTagInnerWidth     = statusTagWidth - 2
	scanIndexWidth          = 3
	infoLabelWidth          = 12
	adapterStatusLabelWidth = 14
)

// Polling and retry intervals for rfkill recovery.
const (
	rfkillRecoveryTimeout  = 2 * time.Second
	rfkillRecoveryInterval = 200 * time.Millisecond
)

// Scan duration defaults and bounds.
const (
	defaultScanDuration = 10 * time.Second
	minScanDuration     = 1 * time.Second
	maxScanDuration     = 300 * time.Second
)

// Agent registration identifiers and defaults.
const (
	agentCapability   = "KeyboardDisplay"
	defaultPinCode    = "0000"
	agentPath         = dbus.ObjectPath("/btctl/agent")
	agentInterface    = "org.bluez.Agent1"
	agentManagerIface = "org.bluez.AgentManager1"
)

// BlueZ and freedesktop D-Bus interface names and paths.
const (
	bluezService           = "org.bluez"
	bluezRootPath          = dbus.ObjectPath("/")
	bluezBasePath          = dbus.ObjectPath("/org/bluez")
	bluezDeviceIface       = "org.bluez.Device1"
	bluezAdapterIface      = "org.bluez.Adapter1"
	bluezBatteryIface      = "org.bluez.Battery1"
	dbusObjectManagerIface = "org.freedesktop.DBus.ObjectManager"
	dbusPropertiesIface    = "org.freedesktop.DBus.Properties"

	signalMemberInterfacesAdded   = "InterfacesAdded"
	signalMemberPropertiesChanged = "PropertiesChanged"

	signalInterfacesAdded   = dbusObjectManagerIface + "." + signalMemberInterfacesAdded
	signalPropertiesChanged = dbusPropertiesIface + "." + signalMemberPropertiesChanged
)

// signalChannelBuffer is the capacity of D-Bus signal delivery channels.
const signalChannelBuffer = 8

// pickerWaitDelay is how long a killed picker may keep its pipes open.
const pickerWaitDelay = time.Second

// Picker backend executable names.
const (
	rofiExecutable  = "rofi"
	dmenuExecutable = "dmenu"
)

// Desktop notification and progress dialog executables.
const (
	notifySendExecutable    = "notify-send"
	zenityExecutable        = "zenity"
	notifyAppName           = "btctl"
	notifyIconBluetooth     = "bluetooth"
	notifyTimeoutPersistent = 0
	notifyTimeoutComplete   = 2000
)

// Battery sentinel when org.bluez.Battery1 is absent.
const batteryUnavailable = -1

// MinScanDuration returns the minimum allowed scan window.
func MinScanDuration() time.Duration { return minScanDuration }

// MaxScanDuration returns the maximum allowed scan window.
func MaxScanDuration() time.Duration { return maxScanDuration }

// DefaultScanDuration returns the default scan window for btctl scan.
func DefaultScanDuration() time.Duration {
	return defaultScanDuration
}

// AgentUnregisterTimeout returns the deadline for deferred agent teardown.
func AgentUnregisterTimeout() time.Duration { return agentUnregisterTimeout }

// StatusClientTimeout returns the deadline for the status subcommand client.
func StatusClientTimeout() time.Duration { return statusClientTimeout }

// stderrPrefix tags operator-facing menu messages.
const stderrPrefix = "[btctl] "

// dmenuTitlePrefix marks the synthetic header line in dmenu pickers.
const dmenuTitlePrefix = "> "

// trustActionLine and untrustActionLine are device info menu actions.
const (
	trustActionLine   = "> trust this device"
	untrustActionLine = "> untrust this device"
)
