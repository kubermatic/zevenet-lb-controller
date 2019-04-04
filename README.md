# Zevenet LoadBalancer Controller

## What does it do

It is a Kubernetes controller that realizes [Services of type LoadBalancer](https://kubernetes.io/docs/concepts/services-networking/#loadbalancer)
for the [Zevenet LoadBalancer Platform](https://www.zevenet.com/)

## Quickstart

* Replace the `https://<<ZEVENET_HOST>>:<<ZEVENET_PORT>>` string in `manifest.yaml` with the address of your Zevenet API
* Create an API Token for your Zevenet installation and `<<ZEVENET_TOKEN>>` string in `manifest.yaml` with it
* If desired: Configure the parent interface for the virtual interfaces by changing the value of the `ZEVENET_PARENT_INTERFACE_NAME` env var in `manifest.yaml`. It will default to `eth0` if unset.
* Install the controller into your cluster: `kubectl apply -f manifest.yaml`
* Create a service of type `LoadBalancer`

**Note:** It is required that all services managed via this controller have the `.spec.loadBalancerIP` field set,
because the controller does not implement an IP Address Management functionality
