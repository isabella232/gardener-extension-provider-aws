// Copyright (c) 2021 SAP SE or an SAP affiliate company. All rights reserved. This file is licensed under the Apache Software License, v. 2 except as noted otherwise in the LICENSE file
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

package integration

import (
	"context"
	"time"

	awsclient "github.com/gardener/gardener-extension-provider-aws/pkg/aws/client"

	awssdk "github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/awserr"
	"github.com/aws/aws-sdk-go/service/ec2"
	"github.com/sirupsen/logrus"
	"k8s.io/apimachinery/pkg/util/wait"
)

// CreateVPC creates a new VPC and waits for it to become available. It returns
// the VPC ID, the Internet Gateway ID or an error in case something unexpected happens.
func CreateVPC(ctx context.Context, logger *logrus.Entry, awsClient *awsclient.Client, vpcCIDR string, enableDnsHostnames bool) (string, string, error) {
	entry := logger.WithField("test", "existing-vpc")

	createVpcOutput, err := awsClient.EC2.CreateVpc(&ec2.CreateVpcInput{
		CidrBlock: awssdk.String(vpcCIDR),
	})
	if err != nil {
		return "", "", err
	}
	vpcID := createVpcOutput.Vpc.VpcId

	if err := wait.PollUntil(5*time.Second, func() (bool, error) {
		entry.Infof("Waiting until vpc '%s' is available...", *vpcID)

		describeVpcOutput, err := awsClient.EC2.DescribeVpcs(&ec2.DescribeVpcsInput{
			VpcIds: []*string{vpcID},
		})
		if err != nil {
			return false, err
		}

		vpc := describeVpcOutput.Vpcs[0]
		if *vpc.State != "available" {
			return false, nil
		}

		return true, nil
	}, ctx.Done()); err != nil {
		return "", "", err
	}

	if enableDnsHostnames {
		_, err = awsClient.EC2.ModifyVpcAttribute(&ec2.ModifyVpcAttributeInput{
			EnableDnsHostnames: &ec2.AttributeBooleanValue{
				Value: awssdk.Bool(true),
			},
			VpcId: vpcID,
		})
		if err != nil {
			return "", "", err
		}
	}

	createIgwOutput, err := awsClient.EC2.CreateInternetGateway(&ec2.CreateInternetGatewayInput{})
	if err != nil {
		return "", "", err
	}
	igwID := createIgwOutput.InternetGateway.InternetGatewayId

	_, err = awsClient.EC2.AttachInternetGateway(&ec2.AttachInternetGatewayInput{
		InternetGatewayId: igwID,
		VpcId:             vpcID,
	})
	if err != nil {
		return "", "", err
	}

	if err := wait.PollUntil(5*time.Second, func() (bool, error) {
		entry.Infof("Waiting until internet gateway '%s' is attached to vpc '%s'...", *igwID, *vpcID)

		describeIgwOutput, err := awsClient.EC2.DescribeInternetGateways(&ec2.DescribeInternetGatewaysInput{
			InternetGatewayIds: []*string{igwID},
		})
		if err != nil {
			return false, err
		}

		igw := describeIgwOutput.InternetGateways[0]
		if len(igw.Attachments) == 0 {
			return false, nil
		}
		if *igw.Attachments[0].State != "available" {
			return false, nil
		}

		return true, nil
	}, ctx.Done()); err != nil {
		return "", "", err
	}

	return *vpcID, *igwID, nil
}

// DestroyVPC deletes the Internet Gateway and the VPC itself.
func DestroyVPC(ctx context.Context, logger *logrus.Entry, awsClient *awsclient.Client, vpcID string) error {
	entry := logger.WithField("test", "existing-vpc")

	describeInternetGatewaysOutput, err := awsClient.EC2.DescribeInternetGatewaysWithContext(ctx, &ec2.DescribeInternetGatewaysInput{Filters: []*ec2.Filter{
		{
			Name: awssdk.String("attachment.vpc-id"),
			Values: []*string{
				awssdk.String(vpcID),
			},
		},
	}})
	if err != nil {
		return err
	}
	igwID := describeInternetGatewaysOutput.InternetGateways[0].InternetGatewayId

	_, err = awsClient.EC2.DetachInternetGateway(&ec2.DetachInternetGatewayInput{
		InternetGatewayId: igwID,
		VpcId:             awssdk.String(vpcID),
	})
	if err != nil {
		return err
	}

	if err := wait.PollUntil(5*time.Second, func() (bool, error) {
		entry.Infof("Waiting until internet gateway '%s' is detached from vpc '%s'...", *igwID, vpcID)

		describeIgwOutput, err := awsClient.EC2.DescribeInternetGateways(&ec2.DescribeInternetGatewaysInput{
			InternetGatewayIds: []*string{igwID},
		})
		if err != nil {
			return false, err
		}
		igw := describeIgwOutput.InternetGateways[0]

		return len(igw.Attachments) == 0, nil
	}, ctx.Done()); err != nil {
		return err
	}

	_, err = awsClient.EC2.DeleteInternetGateway(&ec2.DeleteInternetGatewayInput{
		InternetGatewayId: igwID,
	})
	if err != nil {
		return err
	}

	if err := wait.PollUntil(5*time.Second, func() (bool, error) {
		entry.Infof("Waiting until internet gateway '%s' is deleted...", *igwID)

		_, err := awsClient.EC2.DescribeInternetGateways(&ec2.DescribeInternetGatewaysInput{
			InternetGatewayIds: []*string{igwID},
		})
		if err != nil {
			ec2err, ok := err.(awserr.Error)
			if ok && ec2err.Code() == "InvalidInternetGatewayID.NotFound" {
				return true, nil
			}

			return true, err
		}

		return false, nil
	}, ctx.Done()); err != nil {
		return err
	}

	_, err = awsClient.EC2.DeleteVpc(&ec2.DeleteVpcInput{
		VpcId: &vpcID,
	})
	if err != nil {
		return err
	}

	return wait.PollUntil(5*time.Second, func() (bool, error) {
		entry.Infof("Waiting until vpc '%s' is deleted...", vpcID)

		_, err := awsClient.EC2.DescribeVpcs(&ec2.DescribeVpcsInput{
			VpcIds: []*string{&vpcID},
		})
		if err != nil {
			ec2err, ok := err.(awserr.Error)
			if ok && ec2err.Code() == "InvalidVpcID.NotFound" {
				return true, nil
			}

			return true, err
		}

		return false, nil
	}, ctx.Done())
}
