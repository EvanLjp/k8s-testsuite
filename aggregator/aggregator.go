package main

import (
	"encoding/json"
	"flag"
	"fmt"
	vegeta "github.com/tsenart/vegeta/lib"
	"io/ioutil"
	"net/http"
	"sync"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	apiv1 "k8s.io/client-go/pkg/api/v1"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	"errors"
	"github.com/golang/glog"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
)

type replicas struct {
	title     string
	loadbots  int32
	webserver int32
}

type resultEntry struct {
	title  string
	result string
}

var scenarios = []replicas{
	{
		title:     "20 X",
		loadbots:  20,
		webserver: 20,
	},
}

var summary []resultEntry
var kubeconfig *string
var scaleTestNamespace string
var clientset *kubernetes.Clientset

const (
	summaryDataMarker    = "GENERATING SUMMARY OUTPUT"
	summaryEndDataMarker = "END SUMMARY DATA"

	loadbotsName  = "loadbots"
	webserverName = "webserver"

	maxScaleReplicas = 100
	iterations       = 10
	attempts         = 3
)

var (
	inCluster    = flag.Bool("incluster", true, "Running aggregator inside Kubernetes")
	selector     = flag.String("selector", "app", "The label key as selector for pods")
	loadbotsPort = flag.Int("loadbots-port", 8080, "Target port of selected pods")
	maxReplicas  = flag.Int("max-replicas", maxScaleReplicas, "Maximum replication count per service. Total replicas will be twice as much.")
	sleep        = flag.Duration("sleep", 1*time.Second, "The sleep period between aggregations")
	interval     = flag.Duration("interval", 10*time.Second, "The interval between every test")
)

func main() {
	if home := homeDir(); home != "" {
		kubeconfig = flag.String("kubeconfig", filepath.Join(home, ".kube", "config"), "(optional) absolute path to the kubeconfig file when in-cluster false")
	} else {
		kubeconfig = flag.String("kubeconfig", "", "(optional) absolute path to the kubeconfig file when in-cluster false")
	}

	flag.Parse()
	glog.Info("Creating Kubernetes client")
	createKubernetesClient()
	setNamespace()

	glog.Info("Running preflight checks")
	preflightChecks()
	glog.Info("Finished preflight checks")

	glog.Infof("Running scale test with max replicas %d", *maxReplicas)
	runScaleTest()
	showSummary()
	//scaleReplicationController(scaleTestNamespace, loadbotsName, 0)
	//scaleReplicationController(scaleTestNamespace, webserverName, 0)

	glog.Info("Aggregator finished work")
	exitSignal := make(chan os.Signal)
	signal.Notify(exitSignal, syscall.SIGINT, syscall.SIGTERM)
	<-exitSignal
}

func createKubernetesClient() {
	inClusterConf := ""
	if *inCluster {
		inClusterConf = "in"
	} else {
		inClusterConf = "out of"
	}
	glog.Infof("Creating %s cluster config", inClusterConf)

	var clientsetError error
	if *inCluster {
		config, err := rest.InClusterConfig()
		panicOnError(err)
		clientset, clientsetError = kubernetes.NewForConfig(config)
	} else {
		config, err := clientcmd.BuildConfigFromFlags("", *kubeconfig)
		panicOnError(err)
		clientset, clientsetError = kubernetes.NewForConfig(config)
	}

	panicOnError(clientsetError)

	//v, err := clientset.Discovery().ServerVersion()
	//panicOnError(err)

	//glog.Infof("Running %s Kubernetes Cluster - version v%v.%v (%v) - platform %v",
	//	inClusterConf, v.Major, v.Minor, v.GitVersion, v.Platform)
}

func panicOnError(err error) {
	if err != nil {
		glog.Errorf("Panicing due to error: %s", err)
		panic(err.Error())
	}
}

func setNamespace() {
	if ns := os.Getenv("POD_NAMESPACE"); ns != "" {
		scaleTestNamespace = ns
	} else if data, err := ioutil.ReadFile("/var/run/secrets/kubernetes.io/serviceaccount/namespace"); err == nil {
		if ns := strings.TrimSpace(string(data)); len(ns) > 0 {
			scaleTestNamespace = ns
		}
	}

	if scaleTestNamespace == "" {
		scaleTestNamespace = "default"
	}

	glog.Infof("Running aggregator in namespace %s", scaleTestNamespace)
}

func preflightChecks() {
	glog.Infof("Waiting for initial loadbot and webserver pods to be Running...")
	waitForScaleTestServicesToBeRunning(1, 1)
}

func runScaleTest() {
	var currentLoadbots, currentWebservers int32
	currentLoadbots = 1
	currentWebservers = 1

	scaleReplicationController(scaleTestNamespace, loadbotsName, 0)
	waitForScaleTestServicesToBeRunning(0, currentWebservers)
	currentLoadbots = 0

	for i := 0; i < len(scenarios); i++ {
		s := scenarios[i]
		loadbotReplicas := s.loadbots * int32(*maxReplicas) / 100
		webserverReplicas := s.webserver * int32(*maxReplicas) / 100
		if currentLoadbots != loadbotReplicas {
			scaleReplicationController(scaleTestNamespace, loadbotsName, loadbotReplicas)
			currentLoadbots = loadbotReplicas
		}

		if s.webserver != 1 {
			time.Sleep(1 * time.Second)
			if currentWebservers != webserverReplicas {
				scaleReplicationController(scaleTestNamespace, webserverName, webserverReplicas)
				currentWebservers = webserverReplicas
			}
		} else {
			currentWebservers = 1
			webserverReplicas = 1
		}

		glog.Infof("Load scenario '%s': %d Loadbots - %d Webservers", s.title, currentLoadbots, currentWebservers)
		waitForScaleTestServicesToBeRunning(currentLoadbots, currentWebservers)
		time.Sleep(5 * time.Second)

		parts := []vegeta.Metrics{}
		glog.V(3).Infof("[D] Getting %s pods", loadbotsName)

		loadbots, err := getPods(loadbotsName)
		if err != nil {
			glog.Infof("Error getting loadbot pods: %s", err)
		}

		glog.V(3).Infof("[D] Got %d pods", len(loadbots))
		podNames := ""
		for ix := range loadbots {
			podNames += loadbots[ix].Name + " "
		}
		glog.V(3).Infof("[D] %s", podNames)
		for {
			start := time.Now()
			partsIteration := fetchResults(loadbots)
			if len(partsIteration) < len(loadbots) {
				glog.V(3).Info("[D] Failed to fetch results.")
				latency := time.Since(start)
				if latency < *sleep {
					time.Sleep(*sleep - latency)
				}
			} else {
				parts = append(parts, partsIteration...)
				break
			}
		}

		if len(parts) < len(loadbots) {
			panicOnError(errors.New("failed to fetch results. Quitting aggregator"))
		} else {
			glog.V(4).Infof("[D] Fetched results:\n %s", parts)
			allM, inPerS, outPerS := evaluateData(parts)
			result := fmt.Sprintf("TOTAL: %d  QPS: %.2f Success: %-8.2f%% Latency: %s (mean) %s (99th)"+
				" Bytes: %d (total_in) %d (total_out) %d (input/s) %d (output/s)",
				allM.Requests, allM.Rate, allM.Success,
				allM.Latencies.Mean, allM.Latencies.P99,
				allM.BytesIn.Total, allM.BytesOut.Total, inPerS, outPerS)
			summary = append(summary, resultEntry{
				title:  s.title,
				result: result,
			})
			glog.Infof("Summary of load scenario '%s': %s", s.title, result)
		}
		glog.Infof("Sleep %s for next test case", (*interval).String())
		if i != len(scenarios)-1 {
			scaleReplicationController(scaleTestNamespace, loadbotsName, 0)
			waitForScaleTestServicesToBeRunning(0, currentWebservers)
			currentLoadbots = 0
			time.Sleep(*interval)
		}
	}
}

func fetchResults(loadbots []*apiv1.Pod) []vegeta.Metrics {
	parts := []vegeta.Metrics{}
	lock := sync.Mutex{}
	wg := sync.WaitGroup{}
	wg.Add(len(loadbots))
	for ix, pod := range loadbots {
		glog.V(3).Infof("Get from Pod:%s", pod.Name)
		go func(ix int) {
			defer wg.Done()
			pod := loadbots[ix]
			var data []byte
			if *inCluster {
				url := fmt.Sprintf("http://%s:%d/", pod.Status.PodIP, *loadbotsPort)
				resp, err := http.Get(url)
				if err != nil {
					glog.Infof("Error getting %s: %v", url, err)
					return
				}
				defer resp.Body.Close()
				if data, err = ioutil.ReadAll(resp.Body); err != nil {
					glog.Infof("Error reading response of %s: %v", url, err)
					return
				}
			} else {
				var err error

				url := fmt.Sprintf("/api/v1/namespaces/%s/pods/%s:%d/proxy", scaleTestNamespace, pod.Name, *loadbotsPort)
				data, err = clientset.Discovery().RESTClient().Get().AbsPath(url).DoRaw()
				if err != nil {
					glog.Infof("Error proxying to pod %s: %v", url, err)
					return
				}
			}
			var metrics vegeta.Metrics
			glog.V(3).Infof("Get Metrics:%s", string(data))
			if err := json.Unmarshal(data, &metrics); err != nil {
				glog.Infof("Error decoding: %v\n", err)
				return
			}
			lock.Lock()
			defer lock.Unlock()
			if metrics.Requests > 0 {
				parts = append(parts, metrics)
			}
		}(ix)
	}
	wg.Wait()
	return parts
}

func evaluateData(metrics []vegeta.Metrics) (allM vegeta.Metrics, BytesInPerS uint64, BytesOutPerS uint64) {
	for _, v := range metrics {
		allM.Requests += v.Requests
		if v.Rate > 0 {
			allM.Rate += v.Rate
		}

		allM.Success += v.Success * 100
		allM.Latencies.Mean += v.Latencies.Mean
		allM.Latencies.P99 += v.Latencies.P99
		allM.BytesIn.Total += v.BytesIn.Total
		BytesInPerS = v.BytesIn.Total / uint64(v.Duration.Seconds())
		BytesOutPerS = v.BytesOut.Total / uint64(v.Duration.Seconds())
	}

	allM.Success /= float64(len(metrics))
	allM.Latencies.Mean = time.Duration(allM.Latencies.Mean.Nanoseconds() / int64(len(metrics)))
	allM.Latencies.P99 = time.Duration(allM.Latencies.P99.Nanoseconds() / int64(len(metrics)))
	return
}

func showSummary() {
	glog.Info("Summary of load scenarios:")
	glog.Info(summaryDataMarker)
	for k, s := range summary {
		glog.Infof("%d. %-10s: %s", k, s.title, s.result)
	}
	glog.Infof("%s\n", summaryEndDataMarker)
}

func getPods(appName string) ([]*apiv1.Pod, error) {
	loadbots := []*apiv1.Pod{}

	pods, err := clientset.CoreV1().Pods(scaleTestNamespace).List(metav1.ListOptions{
		LabelSelector: fmt.Sprintf("%s=%s", *selector, appName),
	})
	if err != nil {
		return loadbots, err
	}

	for ix := range pods.Items {
		pod := &pods.Items[ix]
		glog.V(3).Infof(pod.String())
		if pod.Status.PodIP == "" || pod.Status.Phase != apiv1.PodRunning {
			continue
		}
		loadbots = append(loadbots, pod)
	}
	glog.Infof("find %d loadbots", len(loadbots))
	return loadbots, nil
}

func waitForScaleTestServicesToBeRunning(targetLoadbots int32, targetWebserver int32) {
	waitTime := time.Second
	done := false
	for !done {
		if waitTime.Seconds() >= 4 {
			waitTime = time.Second * 4
		}

		loadbotPods, err := clientset.CoreV1().Pods(scaleTestNamespace).List(metav1.ListOptions{
			LabelSelector: fmt.Sprintf("%s=%s", *selector, loadbotsName),
		})
		if err != nil {
			glog.Infof("Error getting list of loadbots: %s", err)
		}
		webserverPods, err := clientset.CoreV1().Pods(scaleTestNamespace).List(metav1.ListOptions{
			LabelSelector: fmt.Sprintf("%s=%s", *selector, webserverName),
		})
		if err != nil {
			glog.Infof("Error getting list of webservers: %s", err)
		}
		lines := int32(len(loadbotPods.Items) + len(webserverPods.Items))

		if lines < targetLoadbots+targetWebserver {
			glog.Infof("Pods status output too short. Waiting %v then checking again.", waitTime)
			time.Sleep(waitTime)
			waitTime *= 2
			continue
		}

		loadbotsRunning := false
		webserverRunning := false
		totalLoadbotsRunning := 0
		totalWebserverRunning := 0
		for _, p := range loadbotPods.Items {
			if targetLoadbots == 0 || p.Status.Phase == apiv1.PodRunning {
				totalLoadbotsRunning++
				if int32(totalLoadbotsRunning) == targetLoadbots {
					loadbotsRunning = true
					break
				}
			}
		}
		if targetLoadbots == 0 {
			loadbotsRunning = totalLoadbotsRunning == 0
		}
		for _, p := range webserverPods.Items {
			if p.Status.Phase == apiv1.PodRunning {
				totalWebserverRunning++
				if int32(totalWebserverRunning) >= targetWebserver {
					webserverRunning = true
					break
				}
			}
		}
		glog.Infof("[D] Running are %v/%v webserver and %v/%v loadbots", totalWebserverRunning, targetWebserver, totalLoadbotsRunning, targetLoadbots)
		if !loadbotsRunning || !webserverRunning {
			glog.V(3).Infof("Pods are not running. Waiting %v then checking again.", waitTime)
			time.Sleep(waitTime)
			waitTime *= 2
		} else {
			done = true
		}
	}
}

func scaleReplicationController(namespace string, name string, replicas int32) error {
	glog.Infof("Scaling %s to %d replicas", name, replicas)
	rc, err := clientset.CoreV1().ReplicationControllers(namespace).Get(name, metav1.GetOptions{})
	if err != nil {
		glog.Infof("Error scaling %s to %d replicas: %s", name, replicas, err)
		return err
	}

	rc.ObjectMeta.SetUID("")
	rc.ObjectMeta.CreationTimestamp.Reset()
	rc.ObjectMeta.SetSelfLink("")
	rc.ObjectMeta.SetResourceVersion("")

	rc.Spec.Replicas = &replicas
	_, err = clientset.CoreV1().ReplicationControllers(namespace).Update(rc)
	if err != nil {
		glog.Infof("Error scaling %s to %d replicas: %s", name, replicas, err)
		return err
	}

	return nil
}

func homeDir() string {
	if h := os.Getenv("HOME"); h != "" {
		return h
	}
	return os.Getenv("USERPROFILE") // windows
}
