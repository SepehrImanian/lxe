package network

import (
	"io/ioutil"
	"os"
	"path/filepath"
	"testing"

	"github.com/automaticserver/lxe/network/libcnifake"
	"github.com/containernetworking/cni/libcni"
	"github.com/containernetworking/cni/pkg/types/current"
	"github.com/stretchr/testify/assert"
)

var (
	// verify interface satisfaction
	_ Plugin           = &cniPlugin{}
	_ PodNetwork       = &cniPodNetwork{}
	_ ContainerNetwork = &cniContainerNetwork{}
)

func fakeCNIFiles(t *testing.T) (string, string, string, string) {
	tmpDir, err := ioutil.TempDir("", "cni")
	assert.NoError(t, err)

	binPath := filepath.Join(tmpDir, DefaultCNIbinPath)
	confPath := filepath.Join(tmpDir, DefaultCNIconfPath)
	netnsPath := filepath.Join(tmpDir, defaultCNInetnsPath)

	err = os.MkdirAll(confPath, 0700)
	assert.NoError(t, err)

	err = ioutil.WriteFile(filepath.Join(confPath, "99-lo.conf"), []byte(`
	{
		"cniVersion": "0.4.0",
		"name": "lo",
		"type": "loopback"
	}`), 0600)
	assert.NoError(t, err)

	err = os.MkdirAll(netnsPath, 0700)
	assert.NoError(t, err)

	return tmpDir, binPath, confPath, netnsPath
}

func TestInitPluginCNI(t *testing.T) {
	t.Parallel()

	tmpDir, binPath, confPath, netnsPath := fakeCNIFiles(t)
	defer os.RemoveAll(tmpDir)

	plugin, err := InitPluginCNI(ConfCNI{
		BinPath:   binPath,
		ConfPath:  confPath,
		NetnsPath: netnsPath,
	})
	assert.NoError(t, err)
	assert.NotNil(t, plugin.cni)
	assert.NotEmpty(t, plugin.conf)
}

func TestConfCNI_setDefaults(t *testing.T) {
	t.Parallel()

	conf := &ConfCNI{}
	conf.setDefaults()
	assert.NotEmpty(t, conf.BinPath)
	assert.NotEmpty(t, conf.ConfPath)
	assert.NotEmpty(t, conf.NetnsPath)
}

func testCNIPlugin(t *testing.T) (*cniPlugin, *libcnifake.FakeCNI, string) {
	fake := &libcnifake.FakeCNI{}
	tmpDir, binPath, confPath, netnsPath := fakeCNIFiles(t)

	return &cniPlugin{
		cni: fake,
		conf: ConfCNI{
			BinPath:   binPath,
			ConfPath:  confPath,
			NetnsPath: netnsPath,
		},
	}, fake, tmpDir
}

func Test_cniPlugin_PodNetwork_Simple(t *testing.T) {
	t.Parallel()

	plugin, _, tmpDir := testCNIPlugin(t)
	defer os.RemoveAll(tmpDir)

	podNet, err := plugin.PodNetwork("foo", nil)
	assert.NoError(t, err)
	assert.NotNil(t, podNet)

	tPodNet := podNet.(*cniPodNetwork)
	assert.NotNil(t, tPodNet.netList)
	assert.NotNil(t, tPodNet.runtimeConf)
}

func Test_cniPlugin_UpdateRuntimeConfig(t *testing.T) {
	t.Parallel()

	plugin, _, tmpDir := testCNIPlugin(t)
	defer os.RemoveAll(tmpDir)

	err := plugin.UpdateRuntimeConfig(nil)
	assert.Error(t, err)
}

// TODO: test getCNINetworkConfig

func Test_cniPlugin_getCNIRuntimeConf(t *testing.T) {
	t.Parallel()

	plugin, _, tmpDir := testCNIPlugin(t)
	defer os.RemoveAll(tmpDir)

	conf := plugin.getCNIRuntimeConf("foo")
	assert.Equal(t, &libcni.RuntimeConf{
		ContainerID: "foo",
		NetNS:       "",
		IfName:      DefaultInterface,
		Args:        [][2]string{},
	}, conf)
}

func testCNIPodNet(t *testing.T) (*cniPodNetwork, *libcnifake.FakeCNI, string) {
	plugin, fake, tmpDir := testCNIPlugin(t)

	return &cniPodNetwork{
		plugin:      plugin,
		netList:     nil,
		runtimeConf: plugin.getCNIRuntimeConf("foo"),
	}, fake, tmpDir
}

func Test_cniPodNetwork_ContainerNetwork(t *testing.T) {
	t.Parallel()

	podNet, _, tmpDir := testCNIPodNet(t)
	defer os.RemoveAll(tmpDir)

	contNet, err := podNet.ContainerNetwork("bar", nil)
	assert.NoError(t, err)
	assert.NotNil(t, contNet)

	tContNet := contNet.(*cniContainerNetwork)
	assert.Equal(t, "bar", tContNet.cid)
}

func Test_cniPodNetwork_Status_Simple(t *testing.T) {
	t.Parallel()

	podNet, _, tmpDir := testCNIPodNet(t)
	defer os.RemoveAll(tmpDir)

	status, err := podNet.Status(ctx, &PropertiesRunning{Properties: Properties{Data: map[string]string{"result": `{"cniVersion":"0.4.0","ips":[{"version":"4","interface":2,"address":"10.22.0.64/16","gateway":"10.22.0.1"}]}`}}})
	assert.NoError(t, err)
	assert.NotNil(t, status)
	assert.Len(t, status.IPs, 1)
	assert.Equal(t, "10.22.0.64", status.IPs[0].String())
}

func Test_cniPodNetwork_Status_Missing(t *testing.T) {
	t.Parallel()

	podNet, _, tmpDir := testCNIPodNet(t)
	defer os.RemoveAll(tmpDir)

	status, err := podNet.Status(ctx, &PropertiesRunning{Properties: Properties{Data: map[string]string{"result": `{"cniVersion":"0.4.0","ips":[]}`}}})
	assert.Error(t, err)
	assert.Nil(t, status)
}

func Test_cniPodNetwork_setup(t *testing.T) {
	t.Parallel()

	podNet, fake, tmpDir := testCNIPodNet(t)
	defer os.RemoveAll(tmpDir)

	netfile := "/proc/5/ns/net"

	fake.AddNetworkListReturns(nil, nil)

	_, err := podNet.setup(ctx, netfile)
	assert.NoError(t, err)
	assert.Equal(t, 1, fake.AddNetworkListCallCount())

	_, _, argRuntimeConf := fake.AddNetworkListArgsForCall(0)
	// assert.Len(t, argConfList.Plugins, 1)
	assert.Equal(t, netfile, argRuntimeConf.NetNS)
}

func Test_cniPodNetwork_teardown_afterSetup(t *testing.T) {
	t.Parallel()

	podNet, fake, tmpDir := testCNIPodNet(t)
	defer os.RemoveAll(tmpDir)

	fake.AddNetworkListReturns(nil, nil)
	fake.DelNetworkListReturns(nil)

	_, err := podNet.setup(ctx, "/proc/5/ns/net")
	assert.NoError(t, err)

	err = podNet.teardown(ctx)
	assert.NoError(t, err)

	assert.Equal(t, 1, fake.AddNetworkListCallCount())
	assert.Equal(t, 1, fake.DelNetworkListCallCount())

	_, _, argRuntimeConf := fake.DelNetworkListArgsForCall(0)
	assert.Equal(t, "", argRuntimeConf.NetNS)
}

func Test_cniPodNetwork_ips_Simple(t *testing.T) {
	t.Parallel()

	podNet, _, tmpDir := testCNIPodNet(t)
	defer os.RemoveAll(tmpDir)

	ips, err := podNet.ips([]byte(`{"ips":[{"address":"10.22.0.64/16"}]}`))
	assert.NoError(t, err)
	assert.Len(t, ips, 1)
	assert.Equal(t, "10.22.0.64", ips[0].String())
}

func Test_cniPodNetwork_ips_Missing(t *testing.T) {
	t.Parallel()

	podNet, _, tmpDir := testCNIPodNet(t)
	defer os.RemoveAll(tmpDir)

	ips, err := podNet.ips([]byte(`{"ips":[{"foo":"bar"}]}`))
	assert.Error(t, err)
	assert.Nil(t, ips)
}

func Test_cniPodNetwork_ips_Invalid(t *testing.T) {
	t.Parallel()

	podNet, _, tmpDir := testCNIPodNet(t)
	defer os.RemoveAll(tmpDir)

	ips, err := podNet.ips([]byte(`{"ips":[{"address":"bar"}]}`))
	assert.Error(t, err)
	assert.Nil(t, ips)
}

func testCNIContNet(t *testing.T) (*cniContainerNetwork, *libcnifake.FakeCNI, string) {
	podNet, fake, tmpDir := testCNIPodNet(t)

	return &cniContainerNetwork{
		pod: podNet,
		cid: "bar",
	}, fake, tmpDir
}

func Test_cniContainerNetwork_WhenStarted(t *testing.T) {
	t.Parallel()

	contNet, fake, tmpDir := testCNIContNet(t)
	defer os.RemoveAll(tmpDir)

	fake.AddNetworkListReturns(&current.Result{CNIVersion: "4.0", IPs: []*current.IPConfig{}}, nil)

	res, err := contNet.WhenStarted(ctx, &PropertiesRunning{Properties: Properties{}, Pid: 6})
	assert.NoError(t, err)
	assert.NotEmpty(t, res.Data)
	assert.Empty(t, res.Nics)
	assert.Empty(t, res.NetworkConfigEntries)
}

func Test_cniContainerNetwork_WhenDeleted(t *testing.T) {
	t.Parallel()

	contNet, fake, tmpDir := testCNIContNet(t)
	defer os.RemoveAll(tmpDir)

	fake.DelNetworkListReturns(nil)

	err := contNet.WhenDeleted(ctx, &Properties{})
	assert.NoError(t, err)
}
