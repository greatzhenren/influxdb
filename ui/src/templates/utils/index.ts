import {
  TemplateType,
  LabelIncluded,
  VariableIncluded,
  Relationships,
  LabelRelationship,
  Label,
  Variable,
} from 'src/types'

export function findIncludedsFromRelationships<
  T extends {id: string; type: TemplateType}
>(
  includeds: {id: string; type: TemplateType}[],
  relationships: {id: string; type: TemplateType}[]
): T[] {
  let intersection = []
  relationships.forEach(r => {
    const included = findIncludedFromRelationship<T>(includeds, r)
    if (included) {
      intersection = [...intersection, included]
    }
  })
  return intersection
}

export function findIncludedFromRelationship<
  T extends {id: string; type: TemplateType}
>(
  included: {id: string; type: TemplateType}[],
  r: {id: string; type: TemplateType}
): T {
  return included.find((i): i is T => i.id === r.id && i.type === r.type)
}

export const findLabelsToCreate = (
  currentLabels: Label[],
  labels: LabelIncluded[]
): LabelIncluded[] => {
  return labels.filter(
    l => !currentLabels.find(el => el.name === l.attributes.name)
  )
}

export const findIncludedVariables = (included: {type: TemplateType}[]) => {
  return included.filter(
    (r): r is VariableIncluded => r.type === TemplateType.Variable
  )
}

export const findVariablesToCreate = (
  existingVariables: Variable[],
  incomingVariables: VariableIncluded[]
): VariableIncluded[] => {
  return incomingVariables.filter(
    v => !existingVariables.find(ev => ev.name === v.attributes.name)
  )
}

export const hasLabelsRelationships = (resource: {
  relationships?: Relationships
}) => !!resource.relationships && !!resource.relationships[TemplateType.Label]

export const getLabelRelationships = (resource: {
  relationships?: Relationships
}): LabelRelationship[] => {
  if (!hasLabelsRelationships(resource)) {
    return []
  }

  return [].concat(resource.relationships[TemplateType.Label].data)
}

export const getIncludedLabels = (included: {type: TemplateType}[]) =>
  included.filter((i): i is LabelIncluded => i.type === TemplateType.Label)

// See https://github.com/influxdata/community-templates/
export const getTemplateUrlDetailsFromGithubSource = (
  url: string
): {directory: string; templateExtension: string; templateName: string} => {
  if (!url.includes('https://github.com/influxdata/community-templates/')) {
    throw new Error(
      "We're only going to fetch from influxdb's github repo right now"
    )
  }
  const [, templatePath] = url.split('/blob/master/')
  const [directory, name] = templatePath.split('/')
  const [templateName, templateExtension] = name.split('.')
  return {
    directory,
    templateExtension,
    templateName,
  }
}

export const getGithubUrlFromTemplateUrlDetails = (
  directory: string,
  templateName: string,
  templateExtension: string
): string => {
  return `https://github.com/influxdata/community-templates/blob/master/${directory}/${templateName}.${templateExtension}`
}
