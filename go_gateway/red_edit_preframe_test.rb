#!/usr/bin/env ruby
# frozen_string_literal: true

require "json"
require "yaml"

root = File.expand_path("..", __dir__)
main_go = File.read(File.join(root, "go_gateway", "main.go"))
service = YAML.load_file(File.join(root, "go_gateway", "configs", "service.yaml"))

endpoint = "/api/v1/generate/red-edit-preframe"
raise "missing #{endpoint} route" unless main_go.include?(endpoint)
raise "route must use red_edit_preframe workflow" unless main_go.include?('handleGenerateWorkflow("red_edit_preframe")')
raise "categories response must expose red_edit_preframe" unless main_go.include?('"red_edit_preframe"') && main_go.include?(endpoint)

yaml_path = File.join(root, "go_gateway", "configs", "workflows", "red_edit_preframe.yaml")
cfg = YAML.load_file(yaml_path)
raise "wrong workflow id" unless cfg["id"] == "red_edit_preframe"
raise "workflow must be image_preframe" unless cfg["mode"] == "image_preframe"
raise "workflow must require input_image" unless cfg["required"].include?("input_image")
raise "workflow must require prompt" unless cfg["required"].include?("prompt")
raise "workflow must point at RED-Edit template" unless cfg["template"].include?("red_edit_preframe.json")

template_path = File.expand_path(cfg["template"], File.dirname(yaml_path))
workflow = JSON.parse(File.read(template_path))
raise "template must be API-shaped" unless workflow.is_a?(Hash) && workflow.values.all? { |node| node.is_a?(Hash) && node["class_type"] && node["inputs"] }

load_image = workflow.values.find { |node| node["class_type"] == "LoadImage" }
raise "missing LoadImage" unless load_image
raise "LoadImage must bind input_image" unless load_image.dig("inputs", "image") == "{{input_image}}"

prompt_node = workflow.values.find { |node| node["class_type"] == "CLIPTextEncode" && node.dig("inputs", "text").to_s.include?("{{prompt}}") }
raise "missing ICEdit prompt binding" unless prompt_node
raise "prompt must use ICEdit diptych instruction" unless prompt_node.dig("inputs", "text").include?("A diptych with two side-by-side images")

unet = workflow.values.find { |node| node["class_type"] == "UNETLoader" }
raise "missing UNETLoader" unless unet
raise "UNETLoader must bind model_name" unless unet.dig("inputs", "unet_name") == "{{model_name}}"

final_save = workflow.values.find do |node|
  node["class_type"] == "SaveImage" && node.dig("inputs", "filename_prefix") == "{{output_prefix}}_after"
end
raise "missing final _after SaveImage" unless final_save

raise "I2V default must stay unchanged" unless service.dig("default_workflows", "image_to_video") == "i2v_wan_remix_single_v30"

puts "red_edit_preframe wiring ok"
