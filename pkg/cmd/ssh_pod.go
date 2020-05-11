// Copyright (c) 2018 SAP SE or an SAP affiliate company. All rights reserved. This file is licensed under the Apache Software License, v. 2 except as noted otherwise in the LICENSE file
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)


// PodInstanceAttribute stores all the critical information for Pod.
type PodInstanceAttribute struct {
	UID                      string
	BastionPodName           string
	NodeName                 string
	Key                      string
	TerraformMain            string
	BastionIPAddress         string
	KubeConfig               string
	InstanceID               string
}

// sshToPod provides cmds to ssh to Nodes via a bastions Pod and clean it up afterwards
func sshToPod(nodeName, path, user, pathSSKeypair string, sshPublicKey []byte) {
	a := &PodInstanceAttribute{}
	a.NodeName = nodeName
	a.UID, _ = ExecCmdReturnOutput("bash", "-c", "whoami")
	a.BastionPodName = "bastion-" + a.UID
	fmt.Printf("Your Bastion Pod name is %s in default namespace.\n", a.BastionPodName)
	a.Key = filepath.Join(pathSSKeypair, "key")
	a.TerraformMain = filepath.Join(pathSSKeypair,"terraform/main.tf")
	a.KubeConfig = filepath.Join(pathSSKeypair,"kubeconfig.yaml")
	a.fetchNodeIP()

	value := podStatusCheck(a.KubeConfig,a.BastionPodName) 
	if value == "Running" {
		a.sshBastionPod()
		a.cleanupBastionPod()
		os.Exit(0)
	} else if value == "Completed" {
		a.cleanupBastionPod()
	}

	a.createBastionPod()
	a.copySSHKey()
	a.sshBastionPod()
	a.cleanupBastionPod()
}

func (a *PodInstanceAttribute) fetchNodeIP() {
	infraType, _:=  ExecCmdReturnOutput("bash", "-c", "head -n 1 " +a.TerraformMain + " | awk '{print $2}' | tr -d '\"' ")
	switch infraType {
	case "aws":
		a.BastionIPAddress = a.NodeName
	case "google":
		arguments := fmt.Sprintf("gcloud compute instances describe %s --flatten=networkInterfaces[0].networkIP", a.NodeName)
		captured := capture()
		operate("gcp", arguments)
		capturedOutput, _ := captured()
		a.BastionIPAddress = findIP(capturedOutput)
	case "azurerm":
		output, err := ExecCmdReturnOutput("bash", "-c", "az vm list-ip-addresses --name " + a.NodeName + " --query '[0].virtualMachine.network.privateIpAddresses' -o json")
		if err != nil {
			fmt.Println("Error az vm list-ip-addresse.")
			os.Exit(2)
		}
		a.BastionIPAddress = findIP(output)
	case "alicloud":			
		a.InstanceID = "i-" + strings.TrimRight(strings.TrimLeft(a.NodeName,"iz"),"z")
		res, err := ExecCmdReturnOutput("bash", "-c", "aliyun ecs DescribeInstanceAttribute --InstanceId='"+a.InstanceID+"'")
		checkError(err)

		decodedQuery := decodeAndQueryFromJSONString(res)
		ips, err := decodedQuery.ArrayOfStrings("VpcAttributes", "PrivateIpAddress", "IpAddress")
		checkError(err)
		a.BastionIPAddress = ips[0]

	case "openstack":
		a.BastionIPAddress = a.NodeName
	default:
		fmt.Println("infrastructure type %s not found", infraType)
		os.Exit(2)
	}		
}
			
func podStatusCheck(kubeconfig,podname string)(result string) {
	arguments := "kubectl --kubeconfig=" +kubeconfig+ " -n default get pod -l run=" + podname + " --output=jsonpath={.items..status.phase}" 
	captured := capture()
	operate("pod", arguments)
	capturedOutput, _ := captured()
	return capturedOutput
}

func (a *PodInstanceAttribute) createBastionPod() {
	// create bastion Pod and completed within 20 mins under default namespace
	fmt.Println("Creating Bastion Pod")
	arguments := "kubectl --kubeconfig=" +a.KubeConfig+ " run --generator=run-pod/v1 -n default " + a.BastionPodName + " --image=alpine --restart=Never -- sleep 1200"
	operate("pod", arguments)
}

// Copy key to Bastion Pod /tmp
func (a *PodInstanceAttribute) copySSHKey() {	
	attemptCnt := 0
	for attemptCnt < 15 {
		if podStatusCheck(a.KubeConfig,a.BastionPodName) == "Running" {
			break 
		}
		time.Sleep(time.Second * 2)
		attemptCnt++
	}

	arguments := "kubectl --kubeconfig=" +a.KubeConfig+ " cp " + a.Key + " default/" + a.BastionPodName + ":/tmp"
	operate("pod", arguments)
}

// SSH node use Baston Pod as Jump box
func (a *PodInstanceAttribute) sshBastionPod() {
	sshCmd := fmt.Sprintf("kubectl --kubeconfig=" +a.KubeConfig+ " exec -it  " + a.BastionPodName + " -- sh -c \"apk add openssh && export PS1=" + a.BastionPodName + "^_^" + "&& cd /tmp && ssh -i key -o StrictHostKeyChecking=no gardener@" + a.BastionIPAddress + " && sh \" ")
	cmd := exec.Command("bash", "-c", sshCmd)
	cmd.Stdout = os.Stdout
	cmd.Stdin = os.Stdin
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		fmt.Println(err)
	}
}

// clean up bastion Pod 
func (a *PodInstanceAttribute) cleanupBastionPod() {
	fmt.Println("Pod Cleanup")
	arguments := "kubectl --kubeconfig=" +a.KubeConfig+ " delete -n default pod/" + a.BastionPodName + " --grace-period=0 --force"
	operate("pod", arguments)
}

