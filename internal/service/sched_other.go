//go:build !linux

package service

func (s *Service) withSched(start func() error) error { return start() }
