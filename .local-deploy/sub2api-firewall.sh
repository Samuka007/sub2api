#!/bin/sh
set -eu

WAN_INTERFACE="eth0"

iptables -N SUB2API-INPUT 2>/dev/null || true
iptables -F SUB2API-INPUT
iptables -A SUB2API-INPUT -m conntrack --ctstate RELATED,ESTABLISHED -j ACCEPT
iptables -A SUB2API-INPUT -i lo -j ACCEPT
iptables -A SUB2API-INPUT -p icmp -j ACCEPT
iptables -A SUB2API-INPUT -i "$WAN_INTERFACE" -p tcp --dport 31589 -j ACCEPT
iptables -A SUB2API-INPUT -i "$WAN_INTERFACE" -p tcp -m multiport --dports 80,443 -j ACCEPT
iptables -A SUB2API-INPUT -i "$WAN_INTERFACE" -p udp --dport 443 -j ACCEPT
iptables -A SUB2API-INPUT -i "$WAN_INTERFACE" -j DROP
iptables -A SUB2API-INPUT -j RETURN
iptables -C INPUT -j SUB2API-INPUT 2>/dev/null || iptables -I INPUT 1 -j SUB2API-INPUT

# Published Docker ports traverse FORWARD, so filter them before Docker's rules.
iptables -N SUB2API-DOCKER 2>/dev/null || true
iptables -F SUB2API-DOCKER
iptables -A SUB2API-DOCKER -m conntrack --ctstate RELATED,ESTABLISHED -j ACCEPT
iptables -A SUB2API-DOCKER -i "$WAN_INTERFACE" -p tcp -m multiport --dports 80,443 -j ACCEPT
iptables -A SUB2API-DOCKER -i "$WAN_INTERFACE" -p udp --dport 443 -j ACCEPT
iptables -A SUB2API-DOCKER -i "$WAN_INTERFACE" -j DROP
iptables -A SUB2API-DOCKER -j RETURN
iptables -C DOCKER-USER -j SUB2API-DOCKER 2>/dev/null || iptables -I DOCKER-USER 1 -j SUB2API-DOCKER

ip6tables -N SUB2API-INPUT 2>/dev/null || true
ip6tables -F SUB2API-INPUT
ip6tables -A SUB2API-INPUT -m conntrack --ctstate RELATED,ESTABLISHED -j ACCEPT
ip6tables -A SUB2API-INPUT -i lo -j ACCEPT
ip6tables -A SUB2API-INPUT -p ipv6-icmp -j ACCEPT
ip6tables -A SUB2API-INPUT -i "$WAN_INTERFACE" -p tcp --dport 31589 -j ACCEPT
ip6tables -A SUB2API-INPUT -i "$WAN_INTERFACE" -p tcp -m multiport --dports 80,443 -j ACCEPT
ip6tables -A SUB2API-INPUT -i "$WAN_INTERFACE" -p udp --dport 443 -j ACCEPT
ip6tables -A SUB2API-INPUT -i "$WAN_INTERFACE" -j DROP
ip6tables -A SUB2API-INPUT -j RETURN
ip6tables -C INPUT -j SUB2API-INPUT 2>/dev/null || ip6tables -I INPUT 1 -j SUB2API-INPUT
