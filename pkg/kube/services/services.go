package services

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jenkins-x/jx-logging/pkg/log"
	"github.com/jenkins-x/jx/v2/pkg/kube"

	"github.com/jenkins-x/jx/v2/pkg/util"
	"github.com/pkg/errors"
	v1 "k8s.io/api/core/v1"
	"k8s.io/api/extensions/v1beta1"
	metaV1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/kubernetes"
	toolsWatch "k8s.io/client-go/tools/watch"
)

const (
	ExposeAnnotation             = "fabric8.io/expose"
	ExposeURLAnnotation          = "fabric8.io/exposeUrl"
	ExposeGeneratedByAnnotation  = "fabric8.io/generated-by"
	ExposeIngressName            = "fabric8.io/ingress.name"
	JenkinsXSkipTLSAnnotation    = "jenkins-x.io/skip.tls"
	ExposeIngressAnnotation      = "fabric8.io/ingress.annotations"
	CertManagerAnnotation        = "certmanager.k8s.io/issuer"
	CertManagerClusterAnnotation = "certmanager.k8s.io/cluster-issuer"
	ServiceAppLabel              = "app"
)

type ServiceURL struct {
	Name string
	URL  string
}

// GetServices returns a list of all services in a given namespace.
func GetServices(client kubernetes.Interface, ns string) (map[string]*v1.Service, error) {
	services := map[string]*v1.Service{}
	list, err := client.CoreV1().Services(ns).List(metaV1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to load Services %s", err)
	}
	for _, r := range list.Items {
		name := r.Name
		r1 := r
		services[name] = &r1
	}
	return services, nil
}

// GetServicesByName returns a list of Service objects from a list of service names.
func GetServicesByName(client kubernetes.Interface, ns string, services []string) ([]*v1.Service, error) {
	answer := make([]*v1.Service, 0)
	svcList, err := client.CoreV1().Services(ns).List(metaV1.ListOptions{})
	if err != nil {
		return answer, errors.Wrapf(err, "listing the services in namespace %q", ns)
	}
	for _, s := range svcList.Items {
		i := util.StringArrayIndex(services, s.GetName())
		if i >= 0 {
			s1 := s
			answer = append(answer, &s1)
		}
	}
	return answer, nil
}

//GetServiceNames returns the names of all the services in a given namespace satisfying a filter.
func GetServiceNames(client kubernetes.Interface, ns string, filter string) ([]string, error) {
	names := []string{}
	list, err := client.CoreV1().Services(ns).List(metaV1.ListOptions{})
	if err != nil {
		return names, fmt.Errorf("failed to load Services %s", err)
	}
	for _, r := range list.Items {
		name := r.Name
		if filter == "" || strings.Contains(name, filter) {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names, nil
}

//FindServiceURL tries to find the  finds the service url. If it fails, it tries to look up the url via ingress.
func FindServiceURL(client kubernetes.Interface, namespace string, name string) (string, error) {
	log.Logger().Debugf("Finding service url for %s in namespace %s", name, namespace)
	svc, err := client.CoreV1().Services(namespace).Get(name, metaV1.GetOptions{})
	if err != nil {
		return "", errors.Wrapf(err, "finding the service %s in namespace %s", name, namespace)
	}

	if svc == nil {
		log.Logger().Debugf("Couldn't find service by name %s", name)
	}

	answer := GetServiceURL(svc)
	if answer != "" {
		log.Logger().Debugf("Found service url %s", answer)
		return answer, nil
	}

	log.Logger().Debugf("Couldn't find service url for %s, attempting to look up via ingress", name)
	// lets try to find the service via Ingress
	url, err := FindIngressURL(client, namespace, name)
	if err != nil {
		log.Logger().Debugf("Unable to find ingress for %s in namespace %s - err %s", name, namespace, err)
		return "", errors.Wrapf(err, "getting ingress for service %q in namespace %s", name, namespace)
	}
	if url == "" {
		log.Logger().Debugf("Unable to find service url via ingress for %s in namespace %s", name, namespace)
	}
	return url, nil
}

func FindIngressURL(client kubernetes.Interface, namespace string, name string) (string, error) {
	log.Logger().Debugf("Finding ingress url for %s in namespace %s", name, namespace)
	// lets try find the service via Ingress
	ing, err := client.ExtensionsV1beta1().Ingresses(namespace).Get(name, metaV1.GetOptions{})
	if err != nil {
		log.Logger().Debugf("Error finding ingress for %s in namespace %s - err %s", name, namespace, err)
		return "", nil
	}

	url := IngressURL(ing)
	if url == "" {
		log.Logger().Debugf("Unable to find url via ingress for %s in namespace %s", name, namespace)
	}
	return url, nil
}

// IngressURL returns the URL for the ingress.
func IngressURL(ing *v1beta1.Ingress) string {
	if ing != nil {
		if len(ing.Spec.Rules) > 0 {
			rule := ing.Spec.Rules[0]
			hostname := rule.Host
			for _, tls := range ing.Spec.TLS {
				for _, h := range tls.Hosts {
					if h != "" {
						url := "https://" + h
						log.Logger().Debugf("found service url %s", url)
						return url
					}
				}
			}
			if hostname != "" {
				url := "http://" + hostname
				log.Logger().Debugf("found service url %s", url)
				return url
			}
		}
	}
	return ""
}

// IngressHost returns the host for the ingress.
func IngressHost(ing *v1beta1.Ingress) string {
	if ing != nil {
		if len(ing.Spec.Rules) > 0 {
			rule := ing.Spec.Rules[0]
			hostname := rule.Host
			for _, tls := range ing.Spec.TLS {
				for _, h := range tls.Hosts {
					if h != "" {
						return h
					}
				}
			}
			if hostname != "" {
				return hostname
			}
		}
	}
	return ""
}

// FindService looks up a service by name across all namespaces.
func FindService(client kubernetes.Interface, name string) (*v1.Service, error) {
	nsl, err := client.CoreV1().Namespaces().List(metaV1.ListOptions{})
	if err != nil {
		return nil, err
	}

	for _, ns := range nsl.Items {
		svc, err := client.CoreV1().Services(ns.GetName()).Get(name, metaV1.GetOptions{})
		if err == nil {
			return svc, nil
		}
	}
	return nil, errors.New("service not found")
}

// GetServiceURL returns the url of the service.
func GetServiceURL(svc *v1.Service) string {
	url := ""
	// Still have the check for svc, because other functions which call svc don't check if svc is nil or not
	if svc != nil && svc.Annotations != nil {
		url = svc.Annotations[ExposeURLAnnotation]
	}
	if url == "" {
		// lets check if its a LoadBalancer
		if svc != nil && svc.Spec.Type == v1.ServiceTypeLoadBalancer {
			scheme := "http"
			for _, port := range svc.Spec.Ports {
				if port.Port == 443 {
					scheme = "https"
					break
				}
			}
			for _, ing := range svc.Status.LoadBalancer.Ingress {
				if ing.IP != "" {
					url = scheme + "://" + ing.IP + "/"
					return url
				}
				if ing.Hostname != "" {
					url = scheme + "://" + ing.Hostname + "/"
					return url
				}
			}
		}
	}
	return url
}

// FindServiceSchemePort parses the service definition and interprets http scheme in the absence of an external ingress.
func FindServiceSchemePort(client kubernetes.Interface, namespace string, name string) (string, string, error) {
	svc, err := client.CoreV1().Services(namespace).Get(name, metaV1.GetOptions{})
	if err != nil {
		return "", "", errors.Wrapf(err, "failed to find service %s in namespace %s", name, namespace)
	}
	return ExtractServiceSchemePort(svc)
}

// Todo: Not sure why we have this function? All it does is call FindServiceURL
func GetServiceURLFromName(c kubernetes.Interface, name, ns string) (string, error) {
	return FindServiceURL(c, ns, name)
}

func FindServiceURLs(client kubernetes.Interface, namespace string) ([]ServiceURL, error) {
	options := metaV1.ListOptions{}
	urls := []ServiceURL{}
	svcs, err := client.CoreV1().Services(namespace).List(options)
	if err != nil {
		return nil, err
	}

	for _, s := range svcs.Items {
		svc := s
		url, err := FindServiceURL(client, namespace, svc.Name)
		if err != nil {
			log.Logger().Debugf("unable to find service url for %s with error %v", svc.Name, err)
		}
		if len(url) > 0 {
			urls = append(urls, ServiceURL{
				Name: svc.Name,
				URL:  url,
			})
		}
	}
	return urls, nil
}

// WaitForExternalIP waits for the pods of a deployment to become ready.
func WaitForExternalIP(client kubernetes.Interface, name, namespace string, timeout time.Duration) error {
	options := metaV1.ListOptions{
		FieldSelector: fields.OneTermEqualSelector("metadata.name", name).String(),
	}

	w, err := client.CoreV1().Services(namespace).Watch(options)
	if err != nil {
		return err
	}
	defer w.Stop()

	condition := func(event watch.Event) (bool, error) {
		svc := event.Object.(*v1.Service)
		return HasExternalAddress(svc), nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	_, err = toolsWatch.UntilWithoutRetry(ctx, w, condition)

	if err == wait.ErrWaitTimeout {
		return fmt.Errorf("service %s never became ready", name)
	}
	return nil
}

// WaitForService waits for a service to become ready.
func WaitForService(client kubernetes.Interface, name, namespace string, timeout time.Duration) error {
	options := metaV1.ListOptions{
		FieldSelector: fields.OneTermEqualSelector("metadata.name", name).String(),
	}

	w, err := client.CoreV1().Services(namespace).Watch(options)
	if err != nil {
		return err
	}
	defer w.Stop()

	condition := func(event watch.Event) (bool, error) {
		svc := event.Object.(*v1.Service)
		return svc.GetName() == name, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	_, err = toolsWatch.UntilWithoutRetry(ctx, w, condition)

	if err == wait.ErrWaitTimeout {
		return fmt.Errorf("service %s never became ready", name)
	}

	return nil
}

// HasExternalAddress checks if load balancer ingress points are IP/DNS based
func HasExternalAddress(svc *v1.Service) bool {
	for _, v := range svc.Status.LoadBalancer.Ingress {
		if v.IP != "" || v.Hostname != "" {
			return true
		}
	}
	return false
}

// CreateServiceLink creates a service of type ServiceTypeExternalName.
func CreateServiceLink(client kubernetes.Interface, currentNamespace, targetNamespace, serviceName,
	externalURL string) error {
	annotations := make(map[string]string)
	annotations[ExposeURLAnnotation] = externalURL

	svc := v1.Service{
		ObjectMeta: metaV1.ObjectMeta{
			Name:        serviceName,
			Namespace:   currentNamespace,
			Annotations: annotations,
		},
		Spec: v1.ServiceSpec{
			Type:         v1.ServiceTypeExternalName,
			ExternalName: fmt.Sprintf("%s.%s.svc.cluster.local", serviceName, targetNamespace),
		},
	}

	_, err := client.CoreV1().Services(currentNamespace).Create(&svc)
	if err != nil {
		return err
	}

	return nil
}

func IsServicePresent(c kubernetes.Interface, name, ns string) (bool, error) {
	svc, err := c.CoreV1().Services(ns).Get(name, metaV1.GetOptions{})
	if err != nil || svc == nil {
		return false, err
	}
	return true, nil
}

// ServiceAppName retrives the application name from service labels. If no app lable exists,
// it returns the service name.
func ServiceAppName(service *v1.Service) string {
	if annotations := service.Annotations; annotations != nil {
		ingName, ok := annotations[ExposeIngressName]
		if ok {
			return ingName
		}
	}
	if labels := service.Labels; labels != nil {
		app, ok := labels[ServiceAppLabel]
		if ok {
			return app
		}
	}
	return service.GetName()
}

// AnnotateServicesWithCertManagerIssuer adds the cert-manager annotation to the services from the given namespace.
// If a list of services is provided, it will apply the annotation only to that specific services.
func AnnotateServicesWithCertManagerIssuer(c kubernetes.Interface, ns, issuer string, clusterIssuer bool,
	services ...string) ([]*v1.Service, error) {
	result := make([]*v1.Service, 0)
	svcList, err := GetServices(c, ns)
	if err != nil {
		return result, err
	}

	for _, s := range svcList {
		// annotate only the services present in the list, if the list is empty annotate all services
		if len(services) > 0 {
			i := util.StringArrayIndex(services, s.GetName())
			if i < 0 {
				continue
			}
		}
		if s.Annotations[ExposeAnnotation] == "true" && s.Annotations[JenkinsXSkipTLSAnnotation] != "true" {
			existingAnnotations := s.Annotations[ExposeIngressAnnotation]
			// if no existing `fabric8.io/ingress.annotations` initialise and add else update with ClusterIssuer
			certManagerAnnotation := CertManagerAnnotation
			if clusterIssuer {
				certManagerAnnotation = CertManagerClusterAnnotation
			}
			if len(existingAnnotations) > 0 {
				s.Annotations[ExposeIngressAnnotation] = existingAnnotations + "\n" + certManagerAnnotation + ": " + issuer
			} else {
				s.Annotations[ExposeIngressAnnotation] = certManagerAnnotation + ": " + issuer
			}
			s, err = c.CoreV1().Services(ns).Update(s)
			if err != nil {
				return result, fmt.Errorf("failed to annotate and update service %s in namespace %s: %v", s.Name, ns, err)
			}
			result = append(result, s)
		}
	}
	return result, nil
}

// AnnotateServicesWithBasicAuth annotates the services with nginx basic auth annotations.
func AnnotateServicesWithBasicAuth(client kubernetes.Interface, ns string, services ...string) error {
	if len(services) == 0 {
		return nil
	}
	svcList, err := GetServices(client, ns)
	if err != nil {
		return errors.Wrapf(err, "retrieving the services from namespace %q", ns)
	}
	for _, service := range svcList {
		// Check if the service is in the white-list
		idx := util.StringArrayIndex(services, service.GetName())
		if idx < 0 {
			continue
		}
		if service.Annotations == nil {
			service.Annotations = map[string]string{}
		}
		// Add the required basic authentication annotation for nginx-ingress controller
		ingressAnnotations := service.Annotations[ExposeIngressAnnotation]
		basicAuthAnnotations := fmt.Sprintf(
			"nginx.ingress.kubernetes.io/auth-type: basic\nnginx.ingress.kubernetes.io/auth-secret: "+
				"%s\nnginx.ingress.kubernetes.io/auth-realm: Authentication is required to access this service",
			kube.SecretBasicAuth)
		if ingressAnnotations != "" {
			ingressAnnotations = ingressAnnotations + "\n" + basicAuthAnnotations
		} else {
			ingressAnnotations = basicAuthAnnotations
		}
		service.Annotations[ExposeIngressAnnotation] = ingressAnnotations
		_, err = client.CoreV1().Services(ns).Update(service)
		if err != nil {
			return errors.Wrapf(err, "updating the service %q in namesapce %q", service.GetName(), ns)
		}
	}
	return nil
}

func CleanServiceAnnotations(c kubernetes.Interface, ns string, services ...string) error {
	svcList, err := GetServices(c, ns)
	if err != nil {
		return err
	}
	for _, s := range svcList {
		// clear the annotations only for the services provided in the list if the list
		// is not empty, otherwise clear the annotations of all services
		if len(services) > 0 {
			i := util.StringArrayIndex(services, s.GetName())
			if i < 0 {
				continue
			}
		}
		if s.Annotations[ExposeAnnotation] == "true" && s.Annotations[JenkinsXSkipTLSAnnotation] != "true" {
			// if no existing `fabric8.io/ingress.annotations` initialise and add else update with ClusterIssuer
			annotationsForIngress := s.Annotations[ExposeIngressAnnotation]
			if len(annotationsForIngress) > 0 {

				var newAnnotations []string
				annotations := strings.Split(annotationsForIngress, "\n")
				for _, element := range annotations {
					annotation := strings.SplitN(element, ":", 2)
					key, _ := annotation[0], strings.TrimSpace(annotation[1])
					if key != CertManagerAnnotation && key != CertManagerClusterAnnotation {
						newAnnotations = append(newAnnotations, element)
					}
				}
				annotationsForIngress = ""
				for _, v := range newAnnotations {
					if len(annotationsForIngress) > 0 {
						annotationsForIngress = annotationsForIngress + "\n" + v
					} else {
						annotationsForIngress = v
					}
				}
				s.Annotations[ExposeIngressAnnotation] = annotationsForIngress

			}
			delete(s.Annotations, ExposeURLAnnotation)

			_, err = c.CoreV1().Services(ns).Update(s)
			if err != nil {
				return fmt.Errorf("failed to clean service %s annotations in namespace %s: %v", s.Name, ns, err)
			}
		}
	}
	return nil
}

// ExtractServiceSchemePort is a utility function to interpret http scheme and port information
// from k8s service definitions.
func ExtractServiceSchemePort(svc *v1.Service) (string, string, error) {
	scheme := ""
	port := ""

	found := false

	// Search in order of degrading priority
	for _, p := range svc.Spec.Ports {
		if p.Port == 443 { // Prefer 443/https if found
			scheme = "https"
			port = "443"
			found = true
			break
		}
	}

	if !found {
		for _, p := range svc.Spec.Ports {
			if p.Port == 80 { // Use 80/http if found
				scheme = "http"
				port = "80"
				found = true
			}
		}
	}

	if !found { // No conventional ports, so search for named https ports
		for _, p := range svc.Spec.Ports {
			if p.Protocol == "TCP" {
				if p.Name == "https" {
					scheme = "https"
					port = strconv.FormatInt(int64(p.Port), 10)
					found = true
					break
				}
			}
		}
	}

	if !found { // No conventional ports, so search for named http ports
		for _, p := range svc.Spec.Ports {
			if p.Name == "http" {
				scheme = "http"
				port = strconv.FormatInt(int64(p.Port), 10)
				found = true
				break
			}
		}
	}

	return scheme, port, nil
}
