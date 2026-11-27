// Package bluez - constants.go collects the package's timeouts, D-Bus names
// and limits so the magic numbers live in one place.
package bluez

import (
	"time"

	"github.com/godbus/dbus/v5"
)

// Scan duration default and bounds, shared by the CLI and the menu.
const (
	DefaultScanDuration = 10 * time.Second
	MinScanDuration     = 1 * time.Second
	MaxScanDuration     = 300 * time.Second
)

// DefaultPinCode is the PIN most legacy (pre-2.1) devices use.
const DefaultPinCode = "0000"

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

	// watchPollInterval is the safety-net poll in watchProperty, which is
	// otherwise driven by PropertiesChanged signals.
	watchPollInterval = time.Second

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

	// agentResponseTimeout is how long agent methods wait for the user; long
	// enough to read the notification and act on the picker.
	agentResponseTimeout = 60 * time.Second

	// servicesResolvedTimeout waits for service discovery after Paired: true.
	servicesResolvedTimeout = 15 * time.Second
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

// Polling and retry intervals for rfkill recovery.
const (
	rfkillRecoveryTimeout  = 2 * time.Second
	rfkillRecoveryInterval = 200 * time.Millisecond
)

// Agent registration identifiers.
const (
	agentCapability   = "KeyboardDisplay"
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

// BatteryUnavailable is Device.Battery when the device reports no battery.
const BatteryUnavailable = -1
