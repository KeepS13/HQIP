package main

import (
	"errors"
	"log"
	"os"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		log.Print(err)
		os.Exit(1)
	}
}
func run(args []string) error {
	if len(args) == 0 {
		return errors.New("用法：hqip serve | init-admin | backup | restore")
	}
	switch args[0] {
	case "migrate-announcements":
		return migrateAnnouncements(args[1:])
	case "migrate-subscription-renewals":
		return migrateSubscriptionRenewals(args[1:])
	case "migrate-traffic-billing":
		return migrateTrafficBilling(args[1:])
	case "migrate-offline-admission":
		return migrateOfflineAdmission(args[1:])
	case "offline-admission":
		return offlineAdmissionSettings(args[1:])
	case "serve":
		return serve(args[1:])
	case "init-admin":
		return initAdmin(args[1:])
	case "backup":
		return backup(args[1:])
	case "restore":
		return restore(args[1:])
	case "storage-info":
		return storageInfo(args[1:])
	case "migrate-payments":
		return migratePayments(args[1:])
	case "migrate-subscription-allocations":
		return migrateSubscriptionAllocations(args[1:])
	case "migrate-operations":
		return migrateOperations(args[1:])
	case "migrate-operation-logs":
		return migrateOperationLogs(args[1:])
	default:
		return errors.New("未知命令；可用命令：serve、init-admin、backup、restore、storage-info、migrate-payments、migrate-subscription-allocations")
	}
}
