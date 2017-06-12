package main_test

import (
	"io/ioutil"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"time"

	"github.com/cloudfoundry-incubator/etcd-release/src/etcdfab/fakes/etcdserver"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/gexec"
)

var _ = Describe("EtcdFab", func() {
	var (
		runDir string

		configFile     *os.File
		linkConfigFile *os.File
		etcdFabCommand *exec.Cmd
		startTLS       bool
	)

	BeforeEach(func() {
		startTLS = true
		tmpDir, err := ioutil.TempDir("", "")
		Expect(err).NotTo(HaveOccurred())

		runDir, err = ioutil.TempDir("", "")
		Expect(err).NotTo(HaveOccurred())

		configFile, err = ioutil.TempFile(tmpDir, "config-file")
		Expect(err).NotTo(HaveOccurred())

		err = configFile.Close()
		Expect(err).NotTo(HaveOccurred())

		writeConfigurationFile(configFile.Name(), map[string]interface{}{
			"node": map[string]interface{}{
				"name":        "some_name",
				"index":       3,
				"external_ip": "some-external-ip",
			},
			"etcd": map[string]interface{}{
				"etcd_path": pathToFakeEtcd,
				"run_dir":   runDir,
				"heartbeat_interval_in_milliseconds": 10,
				"election_timeout_in_milliseconds":   20,
				"peer_require_ssl":                   false,
				"peer_ip":                            "some-peer-ip",
				"require_ssl":                        false,
				"client_ip":                          "some-client-ip",
				"advertise_urls_dns_suffix":          "some-dns-suffix",
				"enable_debug_logging":               true,
			},
		})

		linkConfigFile, err = ioutil.TempFile(tmpDir, "link-config-file")
		Expect(err).NotTo(HaveOccurred())

		err = linkConfigFile.Close()
		Expect(err).NotTo(HaveOccurred())
	})

	AfterEach(func() {
		etcdBackendServer.Reset()
		Expect(os.Remove(configFile.Name())).NotTo(HaveOccurred())
		Expect(os.Remove(linkConfigFile.Name())).NotTo(HaveOccurred())
	})

	Context("when starting etcdfab", func() {
		BeforeEach(func() {
			etcdFabCommand = exec.Command(pathToEtcdFab,
				"--config-file", configFile.Name(),
				"--config-link-file", linkConfigFile.Name(),
			)
		})

		Context("when in non tls mode", func() {
			var (
				etcdServer *etcdserver.EtcdServer
			)

			BeforeEach(func() {
				etcdServer = etcdserver.NewEtcdServer(!startTLS, "")
				etcdServer.SetKeysReturn(http.StatusOK)

				writeConfigurationFile(linkConfigFile.Name(), map[string]interface{}{
					"etcd_path": pathToFakeEtcd,
					"run_dir":   runDir,
					"heartbeat_interval_in_milliseconds": 10,
					"election_timeout_in_milliseconds":   20,
					"peer_require_ssl":                   false,
					"peer_ip":                            "some-peer-ip",
					"require_ssl":                        false,
					"client_ip":                          "some-client-ip",
					"machines":                           []string{"127.0.0.1"},
				})
			})

			AfterEach(func() {
				etcdServer.Exit()
			})

			It("writes etcd stdout/stderr", func() {
				session, err := gexec.Start(etcdFabCommand, GinkgoWriter, GinkgoWriter)
				Expect(err).NotTo(HaveOccurred())
				Eventually(session, 30*time.Second).Should(gexec.Exit(0))

				Expect(string(session.Out.Contents())).To(ContainSubstring("application.build-etcd-flags"))
				Expect(string(session.Out.Contents())).To(ContainSubstring("starting fake etcd"))
				Expect(string(session.Out.Contents())).To(ContainSubstring("stopping fake etcd"))
				Expect(string(session.Err.Contents())).To(ContainSubstring("fake error in stderr"))

				etcdServer.Exit()
			})

			Context("when etcd cluster is synced", func() {
				It("writes a pid and exits 0", func() {
					session, err := gexec.Start(etcdFabCommand, GinkgoWriter, GinkgoWriter)
					Expect(err).NotTo(HaveOccurred())
					Eventually(session, 30*time.Second).Should(gexec.Exit(0))

					pid, err := ioutil.ReadFile(filepath.Join(runDir, "etcd.pid"))
					Expect(err).NotTo(HaveOccurred())

					Expect(strconv.Atoi(string(pid))).To(SatisfyAll(
						BeNumerically(">", 0),
						BeNumerically("<", 4194304),
					))
				})
			})

			Context("when etcd cluster does not sync", func() {
				BeforeEach(func() {
					etcdServer.SetKeysReturn(http.StatusInternalServerError)
				})

				It("does not write a pid and exits 1", func() {
					session, err := gexec.Start(etcdFabCommand, GinkgoWriter, GinkgoWriter)
					Expect(err).NotTo(HaveOccurred())
					Eventually(session, 30*time.Second).Should(gexec.Exit(1))

					Expect(filepath.Join(runDir, "etcd.pid")).NotTo(BeARegularFile())
				})
			})

			Context("when no prior cluster members exist", func() {
				It("starts etcd with proper flags and initial-cluster-state new", func() {
					session, err := gexec.Start(etcdFabCommand, GinkgoWriter, GinkgoWriter)
					Expect(err).NotTo(HaveOccurred())
					Eventually(session, 30*time.Second).Should(gexec.Exit(0))

					Expect(etcdBackendServer.GetCallCount()).To(Equal(1))
					Expect(etcdBackendServer.GetArgs()).To(Equal([]string{
						"--name", "some-name-3",
						"--debug",
						"--data-dir", "/var/vcap/store/etcd",
						"--heartbeat-interval", "10",
						"--election-timeout", "20",
						"--listen-peer-urls", "http://some-peer-ip:7001",
						"--listen-client-urls", "http://some-client-ip:4001",
						"--initial-advertise-peer-urls", "http://some-external-ip:7001",
						"--advertise-client-urls", "http://some-external-ip:4001",
						"--initial-cluster", "some-name-3=http://some-external-ip:7001",
						"--initial-cluster-state", "new",
					}))
				})
			})

			Context("when a prior cluster exists", func() {
				BeforeEach(func() {
					etcdServer.SetMembersReturn(`{
						"members": [
							{
								"id": "some-id",
								"name": "some-name-1",
								"peerURLs": [
									"http://some-other-external-ip:7001"
								]
							}
						]
					}`, http.StatusOK)
					etcdServer.SetAddMemberReturn(`{
						"id": "some-name-3",
						"peerURLs": [
							"http://some-external-ip:7001"
						]
					}`, http.StatusCreated)
				})

				It("starts etcd with proper flags and initial-cluster-state existing", func() {
					session, err := gexec.Start(etcdFabCommand, GinkgoWriter, GinkgoWriter)
					Expect(err).NotTo(HaveOccurred())
					Eventually(session, 30*time.Second).Should(gexec.Exit(0))

					Expect(etcdBackendServer.GetCallCount()).To(Equal(1))
					Expect(etcdBackendServer.GetArgs()).To(Equal([]string{
						"--name", "some-name-3",
						"--debug",
						"--data-dir", "/var/vcap/store/etcd",
						"--heartbeat-interval", "10",
						"--election-timeout", "20",
						"--listen-peer-urls", "http://some-peer-ip:7001",
						"--listen-client-urls", "http://some-client-ip:4001",
						"--initial-advertise-peer-urls", "http://some-external-ip:7001",
						"--advertise-client-urls", "http://some-external-ip:4001",
						"--initial-cluster", "some-name-1=http://some-other-external-ip:7001,some-name-3=http://some-external-ip:7001",
						"--initial-cluster-state", "existing",
					}))
				})
			})
		})

		Context("when configured to be a tls etcd cluster", func() {
			var etcdServer *etcdserver.EtcdServer

			BeforeEach(func() {
				etcdServer = etcdserver.NewEtcdServer(startTLS, "../fixtures")
				etcdServer.SetKeysReturn(http.StatusOK)

				writeConfigurationFile(configFile.Name(), map[string]interface{}{
					"node": map[string]interface{}{
						"name":  "some_name",
						"index": 3,
					},
					"etcd": map[string]interface{}{
						"etcd_path":                          pathToFakeEtcd,
						"run_dir":                            runDir,
						"cert_dir":                           "../fixtures",
						"heartbeat_interval_in_milliseconds": 10,
						"election_timeout_in_milliseconds":   20,
						"peer_require_ssl":                   true,
						"peer_ip":                            "some-peer-ip",
						"require_ssl":                        true,
						"client_ip":                          "some-client-ip",
						"advertise_urls_dns_suffix":          "some-dns-suffix",
						"ca_cert":                            "some-ca-cert",
						"server_cert":                        "some-server-cert",
						"server_key":                         "some-server-key",
						"peer_ca_cert":                       "some-peer-ca-cert",
						"peer_cert":                          "some-peer-cert",
						"peer_key":                           "some-peer-key",
					},
				})
				writeConfigurationFile(linkConfigFile.Name(), map[string]interface{}{
					"etcd_path":                          pathToFakeEtcd,
					"heartbeat_interval_in_milliseconds": 10,
					"election_timeout_in_milliseconds":   20,
					"peer_require_ssl":                   true,
					"peer_ip":                            "some-peer-ip",
					"require_ssl":                        true,
					"client_ip":                          "some-client-ip",
					"advertise_urls_dns_suffix":          "127.0.0.1",
					"enable_debug_logging":               true,
				})
			})

			AfterEach(func() {
				etcdServer.Exit()
			})

			It("shells out to etcd with provided flags", func() {
				session, err := gexec.Start(etcdFabCommand, GinkgoWriter, GinkgoWriter)
				Expect(err).NotTo(HaveOccurred())
				Eventually(session, 30*time.Second).Should(gexec.Exit(0))

				Expect(etcdBackendServer.GetCallCount()).To(Equal(1))
				Expect(etcdBackendServer.GetArgs()).To(Equal([]string{
					"--name", "some-name-3",
					"--debug",
					"--data-dir", "/var/vcap/store/etcd",
					"--heartbeat-interval", "10",
					"--election-timeout", "20",
					"--listen-peer-urls", "https://some-peer-ip:7001",
					"--listen-client-urls", "https://some-client-ip:4001",
					"--initial-advertise-peer-urls", "https://some-name-3.127.0.0.1:7001",
					"--advertise-client-urls", "https://some-name-3.127.0.0.1:4001",
					"--client-cert-auth",
					"--trusted-ca-file", "../fixtures/server-ca.crt",
					"--cert-file", "../fixtures/server.crt",
					"--key-file", "../fixtures/server.key",
					"--peer-client-cert-auth",
					"--peer-trusted-ca-file", "../fixtures/peer-ca.crt",
					"--peer-cert-file", "../fixtures/peer.crt",
					"--peer-key-file", "../fixtures/peer.key",
					"--initial-cluster", "some-name-3=https://some-name-3.127.0.0.1:7001",
					"--initial-cluster-state", "new",
				}))
			})

			It("writes etcd stdout/stderr", func() {
				session, err := gexec.Start(etcdFabCommand, GinkgoWriter, GinkgoWriter)
				Expect(err).NotTo(HaveOccurred())
				Eventually(session, 30*time.Second).Should(gexec.Exit(0))

				Expect(string(session.Out.Contents())).To(ContainSubstring("application.build-etcd-flags"))
				Expect(string(session.Out.Contents())).To(ContainSubstring("starting fake etcd"))
				Expect(string(session.Out.Contents())).To(ContainSubstring("stopping fake etcd"))
				Expect(string(session.Err.Contents())).To(ContainSubstring("fake error in stderr"))
			})
		})

		Context("failure cases", func() {
			Context("when no flags are provided", func() {
				It("exits 1 and prints an error", func() {
					etcdFabCommand := exec.Command(pathToEtcdFab)
					session, err := gexec.Start(etcdFabCommand, GinkgoWriter, GinkgoWriter)
					Expect(err).NotTo(HaveOccurred())
					Eventually(session, 10*time.Second).Should(gexec.Exit(1))

					Expect(string(session.Err.Contents())).To(ContainSubstring("Usage of flags:"))
				})
			})

			Context("when invalid flag is provided", func() {
				It("exits 1 and prints an error", func() {
					etcdFabCommand := exec.Command(pathToEtcdFab,
						"-invalid-flag",
					)
					session, err := gexec.Start(etcdFabCommand, GinkgoWriter, GinkgoWriter)
					Expect(err).NotTo(HaveOccurred())
					Eventually(session, 10*time.Second).Should(gexec.Exit(1))

					Expect(string(session.Err.Contents())).To(ContainSubstring("flag provided but not defined: -invalid-flag"))
				})
			})

			Context("when the config file is invalid", func() {
				BeforeEach(func() {
					etcdBackendServer.EnableFastFail()

					err := ioutil.WriteFile(configFile.Name(), []byte("%%%"), os.ModePerm)
					Expect(err).NotTo(HaveOccurred())
				})

				AfterEach(func() {
					etcdBackendServer.DisableFastFail()
				})

				It("exits 1 and prints an error", func() {
					session, err := gexec.Start(etcdFabCommand, GinkgoWriter, GinkgoWriter)
					Expect(err).NotTo(HaveOccurred())
					Eventually(session, 10*time.Second).Should(gexec.Exit(1))

					Expect(string(session.Err.Contents())).To(ContainSubstring("error during start: invalid character '%' looking for beginning of value"))
				})
			})

			Context("when the link config file is invalid", func() {
				BeforeEach(func() {
					etcdBackendServer.EnableFastFail()

					err := ioutil.WriteFile(linkConfigFile.Name(), []byte("%%%"), os.ModePerm)
					Expect(err).NotTo(HaveOccurred())
				})

				AfterEach(func() {
					etcdBackendServer.DisableFastFail()
				})

				It("exits 1 and prints an error", func() {
					etcdFabCommand := exec.Command(pathToEtcdFab,
						"--config-file", configFile.Name(),
						"--config-link-file", linkConfigFile.Name(),
					)
					session, err := gexec.Start(etcdFabCommand, GinkgoWriter, GinkgoWriter)
					Expect(err).NotTo(HaveOccurred())
					Eventually(session, 10*time.Second).Should(gexec.Exit(1))

					Expect(string(session.Err.Contents())).To(ContainSubstring("error during start: invalid character '%' looking for beginning of value"))
				})
			})

			Context("when the etcd process fails", func() {
				BeforeEach(func() {
					etcdBackendServer.EnableFastFail()

					writeConfigurationFile(configFile.Name(), map[string]interface{}{
						"etcd": map[string]interface{}{
							"etcd_path":                          "bogus",
							"heartbeat_interval_in_milliseconds": 10,
							"election_timeout_in_milliseconds":   20,
							"peer_require_ssl":                   false,
							"peer_ip":                            "some-peer-ip",
							"require_ssl":                        false,
							"client_ip":                          "some-client-ip",
							"advertise_urls_dns_suffix":          "some-dns-suffix",
							"machines":                           []string{"some-ip"},
						},
					})

					writeConfigurationFile(linkConfigFile.Name(), map[string]interface{}{})
				})

				AfterEach(func() {
					etcdBackendServer.DisableFastFail()
				})

				It("exits 1 and prints an error", func() {
					session, err := gexec.Start(etcdFabCommand, GinkgoWriter, GinkgoWriter)
					Expect(err).NotTo(HaveOccurred())
					Eventually(session, 10*time.Second).Should(gexec.Exit(1))

					Expect(string(session.Err.Contents())).To(ContainSubstring("error during start: exec: \"bogus\": executable file not found in $PATH"))
				})
			})
		})
	})
})
